package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	email, password := strings.ToLower(strings.TrimSpace(os.Getenv("ADMIN_EMAIL"))), os.Getenv("ADMIN_PASSWORD")
	if email == "" || len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("ADMIN_EMAIL and ADMIN_PASSWORD (8..72 bytes) are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO users(email,password_hash,role) VALUES ($1,$2,'ADMIN') ON CONFLICT (email) DO NOTHING`, email, string(hash))
	if err != nil {
		return err
	}
	var role string
	if err = db.QueryRow(ctx, `SELECT role::text FROM users WHERE email=$1`, email).Scan(&role); err != nil {
		return err
	}
	if role != "ADMIN" {
		return fmt.Errorf("bootstrap email is already assigned to a non-admin user")
	}
	fmt.Println("Administrator account is ready")
	return nil
}
