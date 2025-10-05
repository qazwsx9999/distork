package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

type templateData map[string]any

type messageDTO struct {
	ID                int64     `json:"id"`
	ChannelID         int64     `json:"channelId"`
	AuthorEmail       string    `json:"authorEmail"`
	AuthorDisplayName string    `json:"authorDisplayName"`
	Content           string    `json:"content"`
	CreatedAt         time.Time `json:"createdAt"`
}

type userDTO struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type channelPayload struct {
	ID        int64     `json:"id"`
	ServerID  int64     `json:"serverId"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Type      string    `json:"type"`
}

type serverPayload struct {
	ID        int64            `json:"id"`
	Slug      string           `json:"slug"`
	Name      string           `json:"name"`
	CreatedAt time.Time        `json:"createdAt"`
	Channels  []channelPayload `json:"channels"`
}

type bootstrapPayload struct {
	User            userDTO         `json:"user"`
	Servers         []serverPayload `json:"servers"`
	ActiveServerID  int64           `json:"activeServerId"`
	ActiveChannelID int64           `json:"activeChannelId"`
	Members         []memberInfo    `json:"members"`
	Messages        []messageDTO    `json:"messages"`
}

type serverState struct {
	templates *template.Template
	db        *sql.DB
	ws        *wsHub
	voice     *voiceState

	mu       sync.RWMutex
	sessions map[string]string // sessionID -> email

	defaultServerID  int64
	defaultChannelID int64
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
		ws:        newWSHub(),
		voice:     newVoiceState(),
		sessions:  make(map[string]string),
	}

	if err := srv.ensureDefaultWorkspace(ctx); err != nil {
		log.Fatalf("ensure default workspace: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join("web", "static")))))
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/login", srv.handleLogin)
	mux.HandleFunc("/signup", srv.handleSignup)
	mux.HandleFunc("/logout", srv.handleLogout)
	mux.HandleFunc("/ws", srv.handleWS)
	mux.HandleFunc("/api/bootstrap", srv.handleBootstrap)
	mux.Handle("/api/servers/", http.StripPrefix("/api/servers/", http.HandlerFunc(srv.handleServerAPI)))
	mux.Handle("/api/channels/", http.StripPrefix("/api/channels/", http.HandlerFunc(srv.handleChannelAPI)))

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

func toMessageDTO(msg chatMessage) messageDTO {
	return messageDTO{
		ID:                msg.ID,
		ChannelID:         msg.ChannelID,
		AuthorEmail:       msg.AuthorEmail,
		AuthorDisplayName: msg.AuthorDisplayName,
		Content:           msg.Content,
		CreatedAt:         msg.CreatedAt,
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

	if err := s.ensureMembership(r.Context(), currentUser.Email); err != nil {
		log.Printf("ensure membership: %v", err)
	}

	payload, err := s.buildBootstrapPayload(r.Context(), currentUser)
	if err != nil {
		log.Printf("bootstrap payload: %v", err)
		http.Error(w, "failed to load workspace", http.StatusInternalServerError)
		return
	}

	serversJSON := template.JS("[]")
	if raw, err := json.Marshal(payload.Servers); err == nil {
		serversJSON = template.JS(raw)
	}

	membersJSON := template.JS("[]")
	if raw, err := json.Marshal(payload.Members); err == nil {
		membersJSON = template.JS(raw)
	}

	messagesJSON := template.JS("[]")
	if raw, err := json.Marshal(payload.Messages); err == nil {
		messagesJSON = template.JS(raw)
	}

	data := templateData{
		"Username":        currentUser.Email,
		"DisplayName":     currentUser.DisplayName,
		"ServersJSON":     serversJSON,
		"MembersJSON":     membersJSON,
		"MessagesJSON":    messagesJSON,
		"ActiveServerID":  payload.ActiveServerID,
		"ActiveChannelID": payload.ActiveChannelID,
	}

	s.renderTemplate(w, http.StatusOK, "app", data)
}

func (s *serverState) buildBootstrapPayload(ctx context.Context, currentUser user) (bootstrapPayload, error) {
	servers, err := s.serversForUser(ctx, currentUser.Email)
	if err != nil {
		return bootstrapPayload{}, err
	}

	if len(servers) == 0 {
		if err := s.ensureMembership(ctx, currentUser.Email); err != nil {
			return bootstrapPayload{}, err
		}
		servers, err = s.serversForUser(ctx, currentUser.Email)
		if err != nil {
			return bootstrapPayload{}, err
		}
	}

	activeServerID := s.defaultServerID
	containsActive := false
	for _, srv := range servers {
		if srv.ID == activeServerID {
			containsActive = true
			break
		}
	}
	if !containsActive && len(servers) > 0 {
		activeServerID = servers[0].ID
	}

	var activeChannelID int64
	serverPayloads := make([]serverPayload, 0, len(servers))

	for _, srv := range servers {
		channels, err := s.channelsForServer(ctx, srv.ID)
		if err != nil {
			return bootstrapPayload{}, err
		}

		chPayloads := make([]channelPayload, 0, len(channels))
		for _, ch := range channels {
			chPayloads = append(chPayloads, channelPayload{
				ID:        ch.ID,
				ServerID:  ch.ServerID,
				Slug:      ch.Slug,
				Name:      ch.Name,
				CreatedAt: ch.CreatedAt,
				Type:      "text",
			})
		}

		if len(chPayloads) == 0 {
			now := time.Now().UTC()
			res, err := s.db.ExecContext(ctx, `INSERT INTO channels (server_id, slug, name, created_at) VALUES (?, ?, ?, ?)`, srv.ID, "general", "general", now)
			if err != nil {
				return bootstrapPayload{}, err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return bootstrapPayload{}, err
			}
			chPayloads = append(chPayloads, channelPayload{ID: id, ServerID: srv.ID, Slug: "general", Name: "general", CreatedAt: now, Type: "text"})
		}

		if srv.ID == activeServerID {
			activeChannelID = chPayloads[0].ID
			if srv.ID == s.defaultServerID {
				for _, ch := range chPayloads {
					if ch.ID == s.defaultChannelID {
						activeChannelID = ch.ID
						break
					}
				}
			}
		}

		serverPayloads = append(serverPayloads, serverPayload{
			ID:        srv.ID,
			Slug:      srv.Slug,
			Name:      srv.Name,
			CreatedAt: srv.CreatedAt,
			Channels:  chPayloads,
		})
	}

	if activeChannelID == 0 && len(serverPayloads) > 0 {
		activeChannelID = serverPayloads[0].Channels[0].ID
	}

	members, err := s.membersForServer(ctx, activeServerID)
	if err != nil {
		return bootstrapPayload{}, err
	}

	messages, err := s.recentMessages(ctx, activeChannelID, 100)
	if err != nil {
		return bootstrapPayload{}, err
	}

	msgDTOs := make([]messageDTO, 0, len(messages))
	for _, msg := range messages {
		msgDTOs = append(msgDTOs, toMessageDTO(msg))
	}

	return bootstrapPayload{
		User: userDTO{
			Email:       currentUser.Email,
			DisplayName: currentUser.DisplayName,
		},
		Servers:         serverPayloads,
		ActiveServerID:  activeServerID,
		ActiveChannelID: activeChannelID,
		Members:         members,
		Messages:        msgDTOs,
	}, nil
}

func (s *serverState) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := s.userFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	payload, err := s.buildBootstrapPayload(r.Context(), currentUser)
	if err != nil {
		log.Printf("bootstrap handler: %v", err)
		http.Error(w, "failed to load data", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode bootstrap: %v", err)
	}
}

func (s *serverState) handleServerAPI(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := s.userFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}

	serverID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid server id", http.StatusBadRequest)
		return
	}

	hasAccess, err := s.userHasServerAccess(r.Context(), currentUser.Email, serverID)
	if err != nil {
		log.Printf("check server access: %v", err)
		http.Error(w, "failed to check permissions", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if len(parts) == 1 || parts[1] == "" {
		switch r.Method {
		case http.MethodGet:
			channels, err := s.channelsForServer(r.Context(), serverID)
			if err != nil {
				log.Printf("list channels: %v", err)
				http.Error(w, "failed to list channels", http.StatusInternalServerError)
				return
			}
			payload := make([]channelPayload, 0, len(channels))
			for _, ch := range channels {
				payload = append(payload, channelPayload{
					ID:        ch.ID,
					ServerID:  ch.ServerID,
					Slug:      ch.Slug,
					Name:      ch.Name,
					CreatedAt: ch.CreatedAt,
					Type:      "text",
				})
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(payload); err != nil {
				log.Printf("encode channels: %v", err)
			}
		default:
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch parts[1] {
	case "members":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		members, err := s.membersForServer(r.Context(), serverID)
		if err != nil {
			log.Printf("list members: %v", err)
			http.Error(w, "failed to list members", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(members); err != nil {
			log.Printf("encode members: %v", err)
		}
	default:
		http.NotFound(w, r)
	}
}

func (s *serverState) handleChannelAPI(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := s.userFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}

	channelID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	ch, exists, err := s.channelByID(r.Context(), channelID)
	if err != nil {
		log.Printf("load channel: %v", err)
		http.Error(w, "failed to load channel", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}

	hasAccess, err := s.userHasServerAccess(r.Context(), currentUser.Email, ch.ServerID)
	if err != nil {
		log.Printf("check channel access: %v", err)
		http.Error(w, "failed to verify access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	switch parts[1] {
	case "messages":
		s.handleChannelMessages(w, r, channelID, currentUser)
	default:
		http.NotFound(w, r)
	}
}

func (s *serverState) handleChannelMessages(w http.ResponseWriter, r *http.Request, channelID int64, currentUser user) {
	switch r.Method {
	case http.MethodGet:
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				if n > 500 {
					n = 500
				}
				limit = n
			}
		}

		messages, err := s.recentMessages(r.Context(), channelID, limit)
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
		if utf8.RuneCountInString(content) > 2000 {
			http.Error(w, "message too long", http.StatusBadRequest)
			return
		}

		msg, err := s.saveMessage(r.Context(), channelID, currentUser.Email, content)
		if err != nil {
			log.Printf("save message: %v", err)
			http.Error(w, "failed to save message", http.StatusInternalServerError)
			return
		}
		if msg.AuthorDisplayName == "" {
			msg.AuthorDisplayName = currentUser.DisplayName
		}

		dto := toMessageDTO(msg)

		s.broadcastMessage(dto)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(dto); err != nil {
			log.Printf("encode message response: %v", err)
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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

		if err := s.ensureMembership(r.Context(), u.Email); err != nil {
			log.Printf("ensure membership: %v", err)
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
