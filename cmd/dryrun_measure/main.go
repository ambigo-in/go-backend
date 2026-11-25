package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load(".env")
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://ambigo:ambigo_dev_password@localhost:5432/ambigo?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("pool err: %v\n", err)
	}
	defer pool.Close()

	var pCreated, wCreated interface{}
	_ = pool.QueryRow(ctx, "SELECT created_at FROM payments ORDER BY created_at DESC LIMIT 1").Scan(&pCreated)
	_ = pool.QueryRow(ctx, "SELECT created_at FROM wallet_transactions ORDER BY created_at DESC LIMIT 1").Scan(&wCreated)
	fmt.Printf("Latest payment: %v\nLatest wallet txn: %v\n", pCreated, wCreated)

	_ = pool.QueryRow(ctx, "SELECT created_at FROM payments ORDER BY created_at ASC LIMIT 1").Scan(&pCreated)
	_ = pool.QueryRow(ctx, "SELECT created_at FROM wallet_transactions ORDER BY created_at ASC LIMIT 1").Scan(&wCreated)
	fmt.Printf("Earliest payment: %v\nEarliest wallet txn: %v\n", pCreated, wCreated)
}
