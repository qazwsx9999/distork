package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type serverInfo struct {
	ID        int64
	Slug      string
	Name      string
	CreatedAt time.Time
}

type channelInfo struct {
	ID        int64
	ServerID  int64
	Slug      string
	Name      string
	CreatedAt time.Time
}

type memberInfo struct {
	Email       string
	DisplayName string
	JoinedAt    time.Time
	Role        string
}

type chatMessage struct {
	ID                int64
	ChannelID         int64
	AuthorEmail       string
	AuthorDisplayName string
	Content           string
	CreatedAt         time.Time
}

func ensureSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}

	const usersTable = `
    CREATE TABLE IF NOT EXISTS users (
        email TEXT PRIMARY KEY,
        display_name TEXT NOT NULL,
        password_hash BLOB NOT NULL,
        created_at TIMESTAMP NOT NULL
    );`
	if _, err := db.ExecContext(ctx, usersTable); err != nil {
		return err
	}

	const serversTable = `
    CREATE TABLE IF NOT EXISTS servers (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        slug TEXT NOT NULL UNIQUE,
        name TEXT NOT NULL,
        created_at TIMESTAMP NOT NULL
    );`
	if _, err := db.ExecContext(ctx, serversTable); err != nil {
		return err
	}

	const serverMembersTable = `
    CREATE TABLE IF NOT EXISTS server_members (
        server_id INTEGER NOT NULL,
        user_email TEXT NOT NULL,
        role TEXT NOT NULL DEFAULT 'member',
        joined_at TIMESTAMP NOT NULL,
        PRIMARY KEY (server_id, user_email),
        FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE,
        FOREIGN KEY(user_email) REFERENCES users(email) ON DELETE CASCADE
    );`
	if _, err := db.ExecContext(ctx, serverMembersTable); err != nil {
		return err
	}

	const channelsTable = `
    CREATE TABLE IF NOT EXISTS channels (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        server_id INTEGER NOT NULL,
        slug TEXT NOT NULL,
        name TEXT NOT NULL,
        created_at TIMESTAMP NOT NULL,
        UNIQUE(server_id, slug),
        FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
    );`
	if _, err := db.ExecContext(ctx, channelsTable); err != nil {
		return err
	}

	const messagesTable = `
    CREATE TABLE IF NOT EXISTS channel_messages (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        channel_id INTEGER NOT NULL,
        author_email TEXT NOT NULL,
        content TEXT NOT NULL,
        created_at TIMESTAMP NOT NULL,
        FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE,
        FOREIGN KEY(author_email) REFERENCES users(email) ON DELETE CASCADE
    );`
	if _, err := db.ExecContext(ctx, messagesTable); err != nil {
		return err
	}

	const messagesIndex = `
    CREATE INDEX IF NOT EXISTS idx_channel_messages_channel_created
    ON channel_messages(channel_id, created_at);
    `
	if _, err := db.ExecContext(ctx, messagesIndex); err != nil {
		return err
	}

	return nil
}

func (s *serverState) ensureDefaultWorkspace(ctx context.Context) error {
	const selectServer = `SELECT id FROM servers WHERE slug = ?`
	row := s.db.QueryRowContext(ctx, selectServer, "home")
	if err := row.Scan(&s.defaultServerID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		now := time.Now().UTC()
		res, err := s.db.ExecContext(ctx, `INSERT INTO servers (slug, name, created_at) VALUES (?, ?, ?)`, "home", "Home", now)
		if err != nil {
			return err
		}
		serverID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		s.defaultServerID = serverID

		_, err = s.db.ExecContext(ctx, `INSERT INTO channels (server_id, slug, name, created_at) VALUES (?, ?, ?, ?)`, serverID, "general", "general", now)
		if err != nil {
			return err
		}
	}

	if s.defaultServerID == 0 {
		row := s.db.QueryRowContext(ctx, selectServer, "home")
		if err := row.Scan(&s.defaultServerID); err != nil {
			return err
		}
	}

	const selectChannel = `SELECT id FROM channels WHERE server_id = ? AND slug = ?`
	row = s.db.QueryRowContext(ctx, selectChannel, s.defaultServerID, "general")
	if err := row.Scan(&s.defaultChannelID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := time.Now().UTC()
		res, err := s.db.ExecContext(ctx, `INSERT INTO channels (server_id, slug, name, created_at) VALUES (?, ?, ?, ?)`, s.defaultServerID, "general", "general", now)
		if err != nil {
			return err
		}
		channelID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		s.defaultChannelID = channelID
	}

	return nil
}

func (s *serverState) ensureMembership(ctx context.Context, email string) error {
	if s.defaultServerID == 0 {
		return fmt.Errorf("default server not initialised")
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO server_members (server_id, user_email, role, joined_at) VALUES (?, ?, 'member', ?)`, s.defaultServerID, email, time.Now().UTC())
	return err
}

func (s *serverState) getUserByEmail(ctx context.Context, email string) (user, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT email, display_name, password_hash, created_at FROM users WHERE email = ?`, email)

	var u user
	if err := row.Scan(&u.Email, &u.DisplayName, &u.PasswordHash, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return user{}, false, nil
		}
		return user{}, false, err
	}

	return u, true, nil
}

func (s *serverState) createUser(ctx context.Context, u user) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO users (email, display_name, password_hash, created_at) VALUES (?, ?, ?, ?)`, u.Email, u.DisplayName, u.PasswordHash, u.CreatedAt); err != nil {
		return err
	}
	return s.ensureMembership(ctx, u.Email)
}

func (s *serverState) saveMessage(ctx context.Context, channelID int64, authorEmail, content string) (chatMessage, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO channel_messages (channel_id, author_email, content, created_at) VALUES (?, ?, ?, ?)`, channelID, authorEmail, content, now)
	if err != nil {
		return chatMessage{}, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return chatMessage{}, err
	}

	row := s.db.QueryRowContext(ctx, `
        SELECT m.id, m.channel_id, m.author_email, u.display_name, m.content, m.created_at
        FROM channel_messages m
        JOIN users u ON u.email = m.author_email
        WHERE m.id = ?
    `, id)

	var msg chatMessage
	if err := row.Scan(&msg.ID, &msg.ChannelID, &msg.AuthorEmail, &msg.AuthorDisplayName, &msg.Content, &msg.CreatedAt); err != nil {
		return chatMessage{}, err
	}

	return msg, nil
}

func (s *serverState) recentMessages(ctx context.Context, channelID int64, limit int) ([]chatMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
        SELECT m.id, m.channel_id, m.author_email, u.display_name, m.content, m.created_at
        FROM channel_messages m
        JOIN users u ON u.email = m.author_email
        WHERE m.channel_id = ?
        ORDER BY m.id DESC
        LIMIT ?
    `, channelID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []chatMessage
	for rows.Next() {
		var msg chatMessage
		if err := rows.Scan(&msg.ID, &msg.ChannelID, &msg.AuthorEmail, &msg.AuthorDisplayName, &msg.Content, &msg.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return msgs, nil
}

func (s *serverState) serversForUser(ctx context.Context, email string) ([]serverInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT srv.id, srv.slug, srv.name, srv.created_at
        FROM servers srv
        JOIN server_members sm ON sm.server_id = srv.id
        WHERE sm.user_email = ?
        ORDER BY srv.name
    `, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []serverInfo
	for rows.Next() {
		var srv serverInfo
		if err := rows.Scan(&srv.ID, &srv.Slug, &srv.Name, &srv.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, srv)
	}
	return result, rows.Err()
}

func (s *serverState) channelsForServer(ctx context.Context, serverID int64) ([]channelInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, server_id, slug, name, created_at
        FROM channels
        WHERE server_id = ?
        ORDER BY created_at
    `, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []channelInfo
	for rows.Next() {
		var ch channelInfo
		if err := rows.Scan(&ch.ID, &ch.ServerID, &ch.Slug, &ch.Name, &ch.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, ch)
	}
	return result, rows.Err()
}

func (s *serverState) membersForServer(ctx context.Context, serverID int64) ([]memberInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT u.email, u.display_name, sm.joined_at, sm.role
        FROM server_members sm
        JOIN users u ON u.email = sm.user_email
        WHERE sm.server_id = ?
        ORDER BY u.display_name
    `, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []memberInfo
	for rows.Next() {
		var m memberInfo
		if err := rows.Scan(&m.Email, &m.DisplayName, &m.JoinedAt, &m.Role); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *serverState) channelByID(ctx context.Context, channelID int64) (channelInfo, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, server_id, slug, name, created_at FROM channels WHERE id = ?`, channelID)

	var ch channelInfo
	if err := row.Scan(&ch.ID, &ch.ServerID, &ch.Slug, &ch.Name, &ch.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return channelInfo{}, false, nil
		}
		return channelInfo{}, false, err
	}

	return ch, true, nil
}

func (s *serverState) userHasServerAccess(ctx context.Context, email string, serverID int64) (bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT 1 FROM server_members WHERE server_id = ? AND user_email = ?`, serverID, email)
	var dummy int
	if err := row.Scan(&dummy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
