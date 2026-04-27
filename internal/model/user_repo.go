// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUserNotFound = errors.New("user not found")
var ErrEmailExists = errors.New("email already registered")

type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
}

type UserRepository struct {
	db *pgxpool.Pool
}

func NewUserRepository(db *pgxpool.Pool) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	user := &User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: passwordHash,
	}
	query := `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, $3)`
	_, err := r.db.Exec(ctx, query, user.ID, user.Email, user.PasswordHash)
	if err != nil {
		// Check for unique violation (PG error code 23505)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrEmailExists
		}
		return nil, err
	}
	return user, nil
}

func (r *UserRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	var user User
	query := `SELECT id, email, password_hash FROM users WHERE email = $1`
	err := r.db.QueryRow(ctx, query, email).Scan(&user.ID, &user.Email, &user.PasswordHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}
