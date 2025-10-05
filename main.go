package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type user struct {
	Email        string
	DisplayName  string
	PasswordHash []byte
	CreatedAt    time.Time
}

type messageDTO struct {
	ID                int64     `json:"id"`
	AuthorEmail       string    `json:"authorEmail"`
	AuthorDisplayName string    `json:"authorDisplayName"`
	Content           string    `json:"content"`
	CreatedAt         time.Time `json:"createdAt"`
}

func toMessageDTO(msg chatMessage) messageDTO {
	return messageDTO{
		ID:                msg.ID,
		AuthorEmail:       msg.AuthorEmail,
		AuthorDisplayName: msg.AuthorDisplayName,
		Content:           msg.Content,
		CreatedAt:         msg.CreatedAt,
	}
}

type templateData map[string]any

type serverState struct {
	templates *template.Template
	db        *sql.DB
	events    *eventBroker

	mu       sync.RWMutex
	sessions map[string]string // sessionID -> email
}

const sessionCookieName = "echosphere_session"

func main() {
	tplPattern := filepath.Join("web", "templates", "*.html")
	templates, err := template.ParseGlob(tplPattern)
	if err != nil {
		log.Fatalf("failed to parse templates: %v", err)
	}

	dbPath := filepath.Join("data", "echosphere.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("ensure data directory: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("database ping: %v", err)
	}
	if err := ensureSchema(ctx, db); err != nil {
		log.Fatalf("database migration: %v", err)
	}

	srv := &serverState{
		templates: templates,
		db:        db,
		events:    newEventBroker(),
		sessions:  make(map[string]string),
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join("web", "static")))))
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/login", srv.handleLogin)
	mux.HandleFunc("/signup", srv.handleSignup)
	mux.HandleFunc("/logout", srv.handleLogout)
	mux.HandleFunc("/events", srv.handleEvents)
	mux.HandleFunc("/api/messages", srv.handleMessages)

	addr := ":" + envOrDefault("PORT", "8080")
	defer func() {
		if err := srv.db.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()

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

	messagesJSON := template.JS("[]")
	if messages, err := s.recentMessages(r.Context(), 100); err != nil {
		log.Printf("load messages: %v", err)
	} else {
		payload := make([]messageDTO, 0, len(messages))
		for _, msg := range messages {
			payload = append(payload, toMessageDTO(msg))
		}
		if raw, err := json.Marshal(payload); err != nil {
			log.Printf("marshal messages: %v", err)
		} else {
			messagesJSON = template.JS(string(raw))
		}
	}

	data := templateData{
		"Username":     currentUser.Email,
		"DisplayName":  currentUser.DisplayName,
		"MessagesJSON": messagesJSON,
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

		u, exists, err := s.getUserByEmail(r.Context(), email)
		if err != nil {
			log.Printf("lookup user %s: %v", email, err)
			s.renderTemplate(w, http.StatusInternalServerError, "login", templateData{"Error": "something went wrong"})
			return
		}

		if !exists || bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)) != nil {
			s.renderTemplate(w, http.StatusUnauthorized, "login", templateData{"Error": "invalid email or password"})
			return
		}

		s.createSession(w, u.Email)
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

		ctx := r.Context()

		if _, exists, err := s.getUserByEmail(ctx, email); err != nil {
			log.Printf("check existing user %s: %v", email, err)
			s.renderTemplate(w, http.StatusInternalServerError, "signup", templateData{"Error": "failed to create account"})
			return
		} else if exists {
			s.renderTemplate(w, http.StatusConflict, "signup", templateData{"Error": "an account with that email already exists"})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("hash password: %v", err)
			s.renderTemplate(w, http.StatusInternalServerError, "signup", templateData{"Error": "failed to create account"})
			return
		}

		newUser := user{
			Email:        email,
			DisplayName:  displayName,
			PasswordHash: hash,
			CreatedAt:    time.Now().UTC(),
		}

		if err := s.createUser(ctx, newUser); err != nil {
			log.Printf("create user %s: %v", email, err)
			s.renderTemplate(w, http.StatusInternalServerError, "signup", templateData{"Error": "failed to create account"})
			return
		}

		s.createSession(w, newUser.Email)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
func (s *serverState) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userFromRequest(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	clientID, ch := s.events.subscribe()
	defer s.events.unsubscribe(clientID)

	if _, err := fmt.Fprint(w, ": connected\n\n"); err == nil {
		flusher.Flush()
	}

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}

			payload, err := json.Marshal(toMessageDTO(msg))
			if err != nil {
				log.Printf("marshal sse message: %v", err)
				continue
			}

			if _, err := fmt.Fprint(w, "event: message\n"); err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *serverState) handleMessages(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := s.userFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				if n > 200 {
					n = 200
				}
				limit = n
			}
		}

		messages, err := s.recentMessages(r.Context(), limit)
		if err != nil {
			log.Printf("load messages: %v", err)
			http.Error(w, "failed to load messages", http.StatusInternalServerError)
			return
		}

		payload := make([]messageDTO, 0, len(messages))
		for _, msg := range messages {
			payload = append(payload, toMessageDTO(msg))
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			log.Printf("encode messages: %v", err)
		}
	case http.MethodPost:
		defer r.Body.Close()

		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		content := strings.TrimSpace(body.Content)
		if content == "" {
			http.Error(w, "message cannot be empty", http.StatusBadRequest)
			return
		}
		if utf8.RuneCountInString(content) > 1000 {
			http.Error(w, "message too long", http.StatusBadRequest)
			return
		}

		msg, err := s.saveMessage(r.Context(), currentUser.Email, content)
		if err != nil {
			log.Printf("save message: %v", err)
			http.Error(w, "failed to save message", http.StatusInternalServerError)
			return
		}
		if msg.AuthorDisplayName == "" {
			msg.AuthorDisplayName = currentUser.DisplayName
		}

		s.events.publish(msg)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(toMessageDTO(msg)); err != nil {
			log.Printf("encode message response: %v", err)
		}
	default:
		w.Header().Set("Allow", "GET, POST")
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

	u, exists, err := s.getUserByEmail(r.Context(), email)
	if err != nil {
		log.Printf("userFromRequest lookup %s: %v", email, err)
		return user{}, false
	}

	if !exists {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
		return user{}, false
	}

	return u, true
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
