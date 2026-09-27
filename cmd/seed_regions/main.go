package main

// One-shot offline seeder: loads data/areas_ap_tg.geojson (states + districts
// of AP and Telangana, precomputed H3 cells) into pricing_regions.
// Idempotent upsert by name. Usage: go run ./cmd/seed_regions
import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"ambigo-backend/internal/ids"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/uber/h3-go/v4"
)

type feature struct {
	Geometry   json.RawMessage `json:"geometry"`
	Properties struct {
		Name    string   `json:"name"`
		Level   string   `json:"level"`
		Parent  string   `json:"parent"`
		OsmType string   `json:"osm_type"`
		OsmID   int64    `json:"osm_id"`
		Display string   `json:"display"`
		Res     int      `json:"res"`
		Cells   []string `json:"cells"`
	} `json:"properties"`
}

func main() {
	_ = godotenv.Load()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("POSTGRES_DSN")
	}
	if dsn == "" {
		dsn = "postgres://ambigo:ambigo_dev_password@localhost:5432/ambigo?sslmode=disable"
	}
	raw, err := os.ReadFile("data/areas_ap_tg.geojson")
	if err != nil {
		fmt.Println("READ ERR", err)
		os.Exit(1)
	}
	var fc struct {
		Features []feature `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		fmt.Println("PARSE ERR", err)
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Println("DB ERR", err)
		os.Exit(1)
	}
	defer pool.Close()

	idByName := map[string]string{}
	// Pass 1: states (parent="").
	for _, pass := range []string{"state", "district"} {
		for _, f := range fc.Features {
			p := f.Properties
			if p.Level != pass || len(p.Cells) == 0 {
				continue
			}
			var id string
			err := pool.QueryRow(ctx, `SELECT id::text FROM pricing_regions WHERE name=$1`, p.Name).Scan(&id)
			if err != nil {
				id = ids.New()
				if _, err := pool.Exec(ctx,
					`INSERT INTO pricing_regions (id, name, osm_type, osm_id, display_name, polygon, cells, cell_res, fetched_at, level, parent_id, source)
					 VALUES ($1::uuid, $2, $3, $4, $5, $6::jsonb, $7::text[], $8, now(), $9, NULL, 'seed:ap_tg v1')`,
					id, p.Name, p.OsmType, p.OsmID, p.Display, f.Geometry, p.Cells, p.Res, p.Level); err != nil {
					fmt.Println("INSERT ERR", p.Name, err)
					continue
				}
				fmt.Println("INSERT", p.Level, p.Name, len(p.Cells), "cells res", p.Res)
			} else {
				if _, err := pool.Exec(ctx,
					`UPDATE pricing_regions SET osm_type=$2, osm_id=$3, display_name=$4, polygon=$5::jsonb, cells=$6::text[], cell_res=$7, fetched_at=now(), level=$8, source='seed:ap_tg v1' WHERE id=$1::uuid`,
					id, p.OsmType, p.OsmID, p.Display, f.Geometry, p.Cells, p.Res, p.Level); err != nil {
					fmt.Println("UPDATE ERR", p.Name, err)
					continue
				}
				fmt.Println("UPDATE", p.Level, p.Name, len(p.Cells), "cells res", p.Res)
			}
			idByName[p.Name] = id
		}
	}
	// Pass 2: link district -> state parent.
	linked := 0
	for _, f := range fc.Features {
		p := f.Properties
		if p.Level != "district" || p.Parent == "" {
			continue
		}
		did, ok1 := idByName[p.Name]
		pid, ok2 := idByName[p.Parent]
		if !ok1 || !ok2 {
			continue
		}
		if _, err := pool.Exec(ctx, `UPDATE pricing_regions SET parent_id=$2::uuid WHERE id=$1::uuid`, did, pid); err != nil {
			fmt.Println("PARENT ERR", p.Name, err)
			continue
		}
		linked++
	}
	fmt.Printf("DONE regions=%d linked=%d\n", len(idByName), linked)

	// Spot check: Banaganapalli must fall in Nandyal.
	var n string
	_ = pool.QueryRow(ctx, `SELECT name FROM pricing_regions WHERE cells @> $1::text[] ORDER BY array_length(cells,1) ASC LIMIT 1`,
		[]string{cellFor(15.3264814, 78.2250071, 7)}).Scan(&n)
	fmt.Println("Banaganapalli resolves to:", n)
	_ = pool.QueryRow(ctx, `SELECT name FROM pricing_regions WHERE cells @> $1::text[] ORDER BY array_length(cells,1) ASC LIMIT 1`,
		[]string{cellFor(17.3850, 78.4867, 7)}).Scan(&n)
	fmt.Println("Hyderabad resolves to:", n)
}

func cellFor(lat, lng float64, res int) string {
	c, err := h3.LatLngToCell(h3.NewLatLng(lat, lng), res)
	if err != nil {
		return ""
	}
	return c.String()
}
