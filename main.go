package main

import (
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type user struct {
	Email        string
	DisplayName  string
	PasswordHash []byte
	CreatedAt    time.Time
}

type templateData map[string]any

type serverState struct {
	templates *template.Template

	mu       sync.RWMutex
	users    map[string]user
	sessions map[string]string // sessionID -> email
}

const sessionCookieName = "echosphere_session"

func main() {
	tplPattern := filepath.Join("web", "templates", "*.html")
	templates, err := template.ParseGlob(tplPattern)
	if err != nil {
		log.Fatalf("failed to parse templates: %v", err)
	}

	srv := &serverState{
		templates: templates,
		users:     make(map[string]user),
		sessions:  make(map[string]string),
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join("web", "static")))))
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/login", srv.handleLogin)
	mux.HandleFunc("/signup", srv.handleSignup)
	mux.HandleFunc("/logout", srv.handleLogout)

	addr := ":" + envOrDefault("PORT", "8080")
	log.Printf("EchoSphere server listening on %s", addr)

	if err := http.ListenAndServe(addr, loggingMiddleware(mux)); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

func (s *serverState) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	currentUser, ok := s.userFromRequest(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := templateData{
		"Username":    currentUser.Email,
		"DisplayName": currentUser.DisplayName,
	}

	s.renderTemplate(w, http.StatusOK, "app", data)
}

func (s *serverState) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, ok := s.userFromRequest(r); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		s.renderTemplate(w, http.StatusOK, "login", nil)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			s.renderTemplate(w, http.StatusBadRequest, "login", templateData{"Error": "invalid form submission"})
			return
		}

		email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
		password := r.FormValue("password")

		s.mu.RLock()
		u, exists := s.users[email]
		s.mu.RUnlock()

		if !exists || bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)) != nil {
			s.renderTemplate(w, http.StatusUnauthorized, "login", templateData{"Error": "invalid email or password"})
			return
		}

		s.createSession(w, email)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *serverState) handleSignup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, ok := s.userFromRequest(r); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		s.renderTemplate(w, http.StatusOK, "signup", nil)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			s.renderTemplate(w, http.StatusBadRequest, "signup", templateData{"Error": "invalid form submission"})
			return
		}

		email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		password := r.FormValue("password")
		confirm := r.FormValue("confirm_password")

		if email == "" || displayName == "" {
			s.renderTemplate(w, http.StatusBadRequest, "signup", templateData{"Error": "all fields are required"})
			return
		}

		if password != confirm {
			s.renderTemplate(w, http.StatusBadRequest, "signup", templateData{"Error": "passwords do not match"})
			return
		}

		if len(password) < 8 {
			s.renderTemplate(w, http.StatusBadRequest, "signup", templateData{"Error": "password must be at least 8 characters"})
			return
		}

		s.mu.Lock()
		if _, exists := s.users[email]; exists {
			s.mu.Unlock()
			s.renderTemplate(w, http.StatusConflict, "signup", templateData{"Error": "an account with that email already exists"})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			s.mu.Unlock()
			log.Printf("hash password: %v", err)
			s.renderTemplate(w, http.StatusInternalServerError, "signup", templateData{"Error": "failed to create account"})
			return
		}

		s.users[email] = user{
			Email:        email,
			DisplayName:  displayName,
			PasswordHash: hash,
			CreatedAt:    time.Now(),
		}
		s.mu.Unlock()

		s.createSession(w, email)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *serverState) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()

		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   false,
			SameSite: http.SameSiteLaxMode,
		})
	}

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *serverState) renderTemplate(w http.ResponseWriter, status int, name string, data templateData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if data == nil {
		data = templateData{}
	}
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render template %s: %v", name, err)
	}
}

func (s *serverState) userFromRequest(r *http.Request) (user, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return user{}, false
	}

	s.mu.RLock()
	email, ok := s.sessions[cookie.Value]
	s.mu.RUnlock()
	if !ok {
		return user{}, false
	}

	s.mu.RLock()
	u, ok := s.users[email]
	s.mu.RUnlock()
	return u, ok
}

func (s *serverState) createSession(w http.ResponseWriter, email string) {
	sessionID := generateSessionID()

	s.mu.Lock()
	s.sessions[sessionID] = email
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  time.Now().Add(12 * time.Hour),
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})
}

func generateSessionID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("failed to generate session id")
	}
	return hex.EncodeToString(buf)
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)
		log.Printf("%s %s %s", r.Method, r.URL.Path, duration)
	})
}
