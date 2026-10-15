// One-time: creates a super_admin row for load-test driver approvals.
// Usage: cd ambigo-backend && $env:ADMIN_MOBILE='7702733783'; go run ./cmd/mkloadadmin
// Safe to rerun (skips if the mobile already has an admin row).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("POSTGRES_DSN")
	}
	if dbURL == "" {
		dbURL = "postgres://ambigo:ambigo_dev_password@localhost:5432/ambigo?sslmode=disable"
	}
	mobile := os.Getenv("ADMIN_MOBILE")
	if mobile == "" {
		fmt.Fprintln(os.Stderr, "ERROR: ADMIN_MOBILE required")
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT true FROM admins WHERE mobile=$1`, mobile).Scan(&exists); err == nil && exists {
		fmt.Println("admin already exists for", mobile)
		return
	}
	username := "loadadmin"
	for i := 0; ; i++ {
		var taken bool
		name := username
		if i > 0 {
			name = fmt.Sprintf("%s%d", username, i)
		}
		if err := pool.QueryRow(ctx, `SELECT true FROM admins WHERE username=$1`, name).Scan(&taken); err != nil || !taken {
			username = name
			break
		}
	}
	_, err = pool.Exec(ctx, `INSERT INTO admins (username, mobile, name, role, active, hashed_password) VALUES ($1,$2,'Load Admin','super_admin',true,'!load-test-no-password-login')`, username, mobile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("created super_admin", username, "for mobile", mobile)
}
