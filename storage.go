package main

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type chatMessage struct {
	ID                int64
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

	const messagesTable = `
    CREATE TABLE IF NOT EXISTS messages (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        author_email TEXT NOT NULL,
        content TEXT NOT NULL,
        created_at TIMESTAMP NOT NULL,
        FOREIGN KEY(author_email) REFERENCES users(email) ON DELETE CASCADE
    );`
	if _, err := db.ExecContext(ctx, messagesTable); err != nil {
		return err
	}

	return nil
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (email, display_name, password_hash, created_at) VALUES (?, ?, ?, ?)`, u.Email, u.DisplayName, u.PasswordHash, u.CreatedAt)
	return err
}

func (s *serverState) saveMessage(ctx context.Context, authorEmail, content string) (chatMessage, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO messages (author_email, content, created_at) VALUES (?, ?, ?)`, authorEmail, content, now)
	if err != nil {
		return chatMessage{}, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return chatMessage{}, err
	}

	row := s.db.QueryRowContext(ctx, `
        SELECT m.id, m.author_email, u.display_name, m.content, m.created_at
        FROM messages m
        JOIN users u ON u.email = m.author_email
        WHERE m.id = ?
    `, id)

	var msg chatMessage
	if err := row.Scan(&msg.ID, &msg.AuthorEmail, &msg.AuthorDisplayName, &msg.Content, &msg.CreatedAt); err != nil {
		return chatMessage{}, err
	}

	return msg, nil
}

func (s *serverState) recentMessages(ctx context.Context, limit int) ([]chatMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
        SELECT m.id, m.author_email, u.display_name, m.content, m.created_at
        FROM messages m
        JOIN users u ON u.email = m.author_email
        ORDER BY m.id DESC
        LIMIT ?
    `, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []chatMessage
	for rows.Next() {
		var msg chatMessage
		if err := rows.Scan(&msg.ID, &msg.AuthorEmail, &msg.AuthorDisplayName, &msg.Content, &msg.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return msgs, nil
}
