// Command seed_loadtokens mints load-test credentials through the real API.
//
// For each test mobile it calls request-otp, reads the code straight from the
// auth_otp table (no SMS involved — run the backend WITHOUT SMS creds so no
// real message is ever sent), verifies, and collects access+refresh tokens.
// Drivers are additionally admin-approved and pinned to a real ambulance type.
//
// Usage (from repo root, backend running locally with migrations applied):
//
//	cd ambigo-backend
//	$env:BASE_URL='http://localhost:8080'
//	$env:API_KEY='<api key>'
//	$env:ADMIN_MOBILE='<existing admin 10-digit mobile>' (OTP login, no password)
//	$env:DATABASE_URL='postgres://ambigo:ambigo_dev_password@localhost:5432/ambigo?sslmode=disable'
//	$env:N_USERS='10'; $env:N_DRIVERS='5'
//	go run ./cmd/seed_loadtokens
//
// Output: ../loadtest/.tokens.json  (git-ignored; never commit real tokens)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func postJSON(base, route, apiKey, token string, body interface{}) (int, map[string]interface{}, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, err
		}
	} else {
		buf.WriteString("{}")
	}
	req, err := http.NewRequest("POST", base+route, &buf)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("status %d non-JSON: %s", resp.StatusCode, string(raw))
	}
	return resp.StatusCode, out, nil
}

type tokenPair struct {
	Mobile       string `json:"mobile"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func readOTP(ctx context.Context, pool *pgxpool.Pool, mobile string) (string, error) {
	var otp string
	// Small retry: request returns 200 once persisted, but be tolerant.
	for i := 0; i < 10; i++ {
		err := pool.QueryRow(ctx, `SELECT otp FROM auth_otp WHERE number=$1 ORDER BY created_at DESC LIMIT 1`, mobile).Scan(&otp)
		if err == nil && otp != "" {
			return otp, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", fmt.Errorf("no OTP found for %s", mobile)
}

func main() {
	ctx := context.Background()
	base := envOr("BASE_URL", "http://localhost:8080")
	apiKey := os.Getenv("API_KEY")
	dbURL := envOr("DATABASE_URL", os.Getenv("POSTGRES_DSN"))
	if dbURL == "" {
		dbURL = "postgres://ambigo:ambigo_dev_password@localhost:5432/ambigo?sslmode=disable"
	}
	nUsers := atoiOr(envOr("N_USERS", "10"), 10)
	nDrivers := atoiOr(envOr("N_DRIVERS", "5"), 5)
	outPath := envOr("OUT", "../loadtest/.tokens.json")

	pool, err := pgxpool.New(ctx, dbURL)
	failIf(err)
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		failIf(fmt.Errorf("DB unreachable at %s: %w", dbURL, err))
	}

	// --- users ---
	users := make([]tokenPair, 0, nUsers)
	for i := 1; i <= nUsers; i++ {
		mobile := fmt.Sprintf("99970%05d", i)
		mustPost(base, "/api/v2/auth/user/request-otp", apiKey, "", map[string]string{"mobile": mobile}, 200, "user "+mobile+" request-otp")
		otp, err := readOTP(ctx, pool, mobile)
		failIf(err)
		res := mustPost(base, "/api/v2/auth/user/verify-otp", apiKey, "", map[string]string{
			"mobile": mobile, "otp": otp, "name": fmt.Sprintf("Load User %d", i),
		}, 200, "user "+mobile+" verify")
		users = append(users, tokenPair{mobile, strOf(res, "access_token"), strOf(res, "refresh_token")})
		fmt.Printf("user %-12s ok\n", mobile)
	}

	// --- ambulance type for drivers (create a default if table is empty) ---
	var ambTypeID, ambTypeName string
	if err := pool.QueryRow(ctx, `SELECT id::text, name FROM ambulance_types ORDER BY name LIMIT 1`).Scan(&ambTypeID, &ambTypeName); err != nil {
		fmt.Println("ambulance_types empty — inserting default Patient Transportation type")
		tiers := `[{"threshold_distance":3,"cost_per_km":0},{"threshold_distance":10,"cost_per_km":28},{"threshold_distance":30,"cost_per_km":25},{"threshold_distance":1000,"cost_per_km":23}]`
		if err := pool.QueryRow(ctx, `INSERT INTO ambulance_types (name, base_fare, pricing_tier, driver_share, listing_threshold, helper_included, otp_required)
			VALUES ('Patient Transportation', 600, $1::jsonb, 95, 20, false, true) RETURNING id::text`, tiers).Scan(&ambTypeID); err != nil {
			failIf(fmt.Errorf("insert default ambulance type: %w", err))
		}
		ambTypeName = "Patient Transportation"
	}
	fmt.Printf("drivers pinned to type %s (%s)\n", ambTypeName, ambTypeID)

	// --- admin token via mobile OTP (admins have no password login here) ---
	adminMobile := os.Getenv("ADMIN_MOBILE")
	var adminToken string
	if nDrivers > 0 {
		if adminMobile == "" {
			failIf(fmt.Errorf("ADMIN_MOBILE required (10-digit mobile of an existing active admin)"))
		}
		mustPost(base, "/api/v2/admin/login/mobile", apiKey, "", map[string]string{"mobile": adminMobile}, 200, "admin request-otp")
		otp, err := readOTP(ctx, pool, adminMobile)
		failIf(err)
		res := mustPost(base, "/api/v2/admin/login/mobile/verify", apiKey, "", map[string]string{"mobile": adminMobile, "otp": otp}, 200, "admin verify")
		adminToken = strOf(res, "access_token")
	}

	// --- drivers ---
	drivers := make([]tokenPair, 0, nDrivers)
	for i := 1; i <= nDrivers; i++ {
		mobile := fmt.Sprintf("99971%05d", i)
		name := fmt.Sprintf("Load Driver %d", i)

		// Already verified (rerun-safe)? Then just log in.
		var already bool
		_ = pool.QueryRow(ctx, `SELECT true FROM drivers WHERE mobile=$1`, mobile).Scan(&already)
		if !already {
			mustPost(base, "/api/v2/auth/driver/request-otp", apiKey, "", map[string]string{"mobile": mobile}, 200, "driver "+mobile+" request-otp")
			otp, err := readOTP(ctx, pool, mobile)
			failIf(err)
			mustPost(base, "/api/v2/auth/driver/verify-otp", apiKey, "", map[string]string{
				"mobile": mobile, "otp": otp, "name": name,
			}, 200, "driver "+mobile+" first verify")
			var unvrfID string
			if err := pool.QueryRow(ctx, `SELECT id::text FROM unverified_drivers WHERE mobile=$1`, mobile).Scan(&unvrfID); err != nil {
				failIf(fmt.Errorf("driver %s unverified row missing: %w", mobile, err))
			}
			// Accept needs the FULL driver object (admin app fetches first);
			// sending only {"_id"} would approve with an empty mobile.
			full := mustPost(base, "/api/v2/admin/drivers/unverified/fetch", apiKey, adminToken, map[string]string{"id": unvrfID}, 200, "driver "+mobile+" fetch")
			mustPost(base, "/api/v2/admin/drivers/unverified/accept", apiKey, adminToken, full, 200, "driver "+mobile+" accept")
			if _, err := pool.Exec(ctx, `UPDATE drivers SET vehicle_type=$1 WHERE mobile=$2`, ambTypeID, mobile); err != nil {
				failIf(fmt.Errorf("driver %s vehicle_type: %w", mobile, err))
			}
		}
		// (Re)login as verified driver.
		mustPost(base, "/api/v2/auth/driver/request-otp", apiKey, "", map[string]string{"mobile": mobile}, 200, "driver "+mobile+" login otp")
		otp, err := readOTP(ctx, pool, mobile)
		failIf(err)
		res := mustPost(base, "/api/v2/auth/driver/verify-otp", apiKey, "", map[string]string{
			"mobile": mobile, "otp": otp, "name": name,
		}, 200, "driver "+mobile+" login verify")
		drivers = append(drivers, tokenPair{mobile, strOf(res, "access_token"), strOf(res, "refresh_token")})
		fmt.Printf("driver %-12s ok\n", mobile)
	}

	out, _ := json.MarshalIndent(map[string]interface{}{"users": users, "drivers": drivers}, "", "  ")
	if err := os.WriteFile(outPath, out, 0600); err != nil {
		failIf(err)
	}
	fmt.Printf("wrote %d users + %d drivers to %s\n", len(users), len(drivers), outPath)
}

// mustPost POSTs and exits on transport error or unexpected status.
// 429s (OTP per-IP limiter: 3/s burst 5) are retried with backoff.
func mustPost(base, route, apiKey, token string, body interface{}, want int, what string) map[string]interface{} {
	var st int
	var res map[string]interface{}
	var err error
	for attempt := 0; attempt < 12; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		st, res, err = postJSON(base, route, apiKey, token, body)
		if err != nil {
			failIf(fmt.Errorf("%s: %w", what, err))
		}
		if st != 429 {
			break
		}
		fmt.Printf("%s: rate-limited, retrying...\n", what)
	}
	if st != want {
		failIf(fmt.Errorf("%s: status %d: %v", what, st, res))
	}
	return res
}

func strOf(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func failIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
