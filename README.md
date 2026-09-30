# 🚑 Ambigo V2 — Go Ambulance Booking Backend

A production-ready, high-performance Go backend for an ambulance on-demand platform with real-time WebSocket support, payment processing, and advanced geospatial matching.

![Go](https://img.shields.io/badge/Go-1.26.4-00ADD8?style=flat-square&logo=go)
![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)
![Build Status](https://img.shields.io/badge/Build-Passing-brightgreen?style=flat-square)
![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat-square)

---

## 📋 Table of Contents

- [Overview](#overview)
- [Key Features](#key-features)
- [System Architecture](#system-architecture)
- [Tech Stack](#tech-stack)
- [Getting Started](#getting-started)
- [Configuration](#configuration)
- [API Documentation](#api-documentation)
- [WebSocket Integration](#websocket-integration)
- [Database Setup](#database-setup)
- [Development](#development)
- [Deployment](#deployment)
- [Project Structure](#project-structure)
- [Contributing](#contributing)

---

## 🎯 Overview

**Ambigo V2** is a comprehensive backend service for an ambulance booking platform that connects patients (users) with verified ambulance drivers in real-time. Built with Go's high-performance networking capabilities, it handles:

- **OTP-based authentication** for users, drivers, and admins
- **Real-time ride matching** using H3 geospatial indexing
- **Live driver-patient communication** via WebSocket
- **Payment processing** through Razorpay and cash payments
- **Driver verification and wallet management**
- **Promotional offers and referral rewards**
- **Comprehensive admin dashboard backend**

| Metric | Value |
|--------|-------|
| REST Endpoints | 63 |
| WebSocket Endpoints | 1 |
| Functional Modules | 14 |
| Supported Databases | 2 (PostgreSQL + MongoDB) |

---

## ✨ Key Features

### 🔐 Authentication & Authorization
- **Multi-role system**: `user`, `driver`, `unverified_driver`, `admin`
- **JWT-based access control** with short-lived tokens
- **Refresh token rotation** with chain lineage tracking
- **OTP verification** via SMS (SMS Country API)
- **Session management** with revocation support

### 🗺️ Real-Time Ride Matching
- **H3 geospatial indexing** for efficient driver location queries
- **EventBus pub/sub** for ride broadcasts
- **Intelligent ride matching algorithm** with acceptance/rejection tracking
- **Route optimization** using Google Maps/Routes API
- **ETA and fare estimation**

### 🚗 Ride Lifecycle Management
- Complete state machine: `requested` → `accepted` → `arrived` → `started` → `completed`
- **OTP verification** at ride start for security
- **Real-time location tracking** via WebSocket
- **Ride history and analytics**
- **Feedback and rating system**

### 💳 Payment Processing
- **Razorpay integration** for online payments
- **Cash payment workflow** with driver confirmation
- **Payment status tracking** and reconciliation
- **Webhook handling** with HMAC signature verification
- **Wallet management** for drivers with withdrawal support
- **Zwitch integration** for bank payouts

### 👨‍💼 Driver Management
- **Verification document upload** (license, insurance, etc.)
- **KYC (Know Your Customer)** via Zwitch
- **Wallet balance tracking** and earnings management
- **Performance metrics** and ride history
- **Verification status transitions**

### 🎁 Business Features
- **Promotional offers** creation and tracking
- **Referral rewards system** with configurable incentives
- **FCM push notifications** for ride updates
- **Feedback and ratings** for quality assurance

### 📊 Monitoring & Observability
- **Prometheus metrics** endpoint for monitoring
- **Request ID tracking** for distributed logging
- **Health check endpoint** with dependency status
- **Rate limiting** (100 req/200s global)

### 🛡️ Enterprise-Grade Security
- **API Key authentication** for all public endpoints
- **CORS middleware** for mobile app support
- **Request body size limiting** (default: 50MB)
- **HMAC-verified webhooks** from payment providers
- **Non-root Docker execution** (UID 65534)

---

## 🏗️ System Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                        Client Layer                               │
│  ┌──────────────┐      ┌──────────────┐      ┌──────────────┐   │
│  │ Flutter App  │      │  Admin Portal │      │  Web Client  │   │
│  │  (User/Drv)  │      │   (Web/Mobile)│      │  (Optional)  │   │
│  └──────┬───────┘      └────────┬──────┘      └──────┬───────┘   │
│         │                       │                      │           │
└─────────┼───────────────────────┼──────────────────────┼───────────┘
          │                       │                      │
          │                       │                      │
          ▼                       ▼                      ▼
┌─────────────────────────────────────────────────────────────────┐
│                  Go HTTP Server (Port 8080)                      │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │ Middleware Stack:                                         │  │
│  │ • CORS • RequestID • Metrics • BodyLimit • RateLimit      │  │
│  │ • API Key Auth • JWT Auth                                 │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ HTTP Router & Handler Modules:                           │   │
│  │ • Auth (OTP/JWT)         • Rides (13 endpoints)          │   │
│  │ • Profile (User/Driver)  • Payment (Razorpay/Cash)       │   │
│  │ • Wallet & Withdrawal    • Admin (20 endpoints)          │   │
│  │ • Verification           • Offers & Referral             │   │
│  │ • Feedback               • Shared (Hospitals, Types)     │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                   │
│  ┌──────────────────┐  ┌─────────────────────────────────┐     │
│  │  WebSocket Hub   │  │   In-Memory EventBus            │     │
│  │ • Connection Pool│  │ • LOCATION_UPDATE               │     │
│  │ • Message Queue  │  │ • RIDE_REQUESTED                │     │
│  │ • Broadcast      │  │ • RIDE_UPDATE                   │     │
│  │ • Ping/Pong      │  │ • Listeners (WebSocket, FCM,    │     │
│  │                  │  │   Audit Log, etc.)              │     │
│  └──────────────────┘  └─────────────────────────────────┘     │
│                                                                   │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ Business Logic Modules:                                  │   │
│  │ • Ride Dispatcher & Matching        • Geospatial (H3)   │   │
│  │ • Pricing Engine (Fare Calculation) • Telephony (Calls) │   │
│  │ • Notification (FCM)                • Translation        │   │
│  │ • Payment Processing                • Referral System    │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
        │                 │                    │
        ▼                 ▼                    ▼
┌──────────────────┐ ┌──────────────┐ ┌────────────────────┐
│  PostgreSQL 16   │ │  MongoDB     │ │ External Services  │
│  (Primary DB)    │ │  (Legacy)    │ │ • Google Maps/     │
│  • Users         │ │  • Metadata  │ │   Routes API       │
│  • Rides         │ │  • Archives  │ │ • Firebase FCM     │
│  • Payments      │ │              │ │ • Razorpay         │
│  • Wallet        │ │              │ │ • Zwitch (Payouts) │
│  • Sessions      │ │              │ │ • SMS Country      │
│  • Verification  │ │              │ │ • Cloudshope       │
│  • Referrals     │ │              │ │ • Google Translate │
└──────────────────┘ └──────────────┘ └────────────────────┘
```

---

## 🛠️ Tech Stack

### Core
- **Language**: Go 1.26.4
- **HTTP Framework**: `net/http` with custom multiplexer
- **WebSocket**: `gorilla/websocket`
- **Router**: Custom HTTP mux with route registration pattern

### Database
- **Primary**: PostgreSQL 16 (via `github.com/jackc/pgx/v5`)
- **Legacy**: MongoDB (via `go.mongodb.org/mongo-driver`)
- **Migrations**: `github.com/pressly/goose/v3`
- **Query Builder**: sqlc (type-safe SQL)

### Authentication & Security
- **JWT**: `github.com/golang-jwt/jwt/v5`
- **Password Hashing**: `golang.org/x/crypto`
- **Validation**: `github.com/go-playground/validator/v10`

### Geospatial & Routing
- **Geospatial Indexing**: `github.com/uber/h3-go/v4` (H3 hexagonal grid)
- **Route Calculation**: Google Maps Routes API
- **Location Services**: Google Places API, Google Maps Distance Matrix

### Payment & Payments
- **Razorpay**: `github.com/razorpay/razorpay-go`
- **Payout Integration**: Zwitch API
- **Call Masking**: Cloudshope API

### Notifications & Communication
- **Push Notifications**: Firebase FCM (Cloud Messaging)
- **SMS**: SMS Country API
- **Translation**: Google Cloud Translation API

### Observability & Monitoring
- **Metrics**: `github.com/prometheus/client_golang`
- **Logging**: `github.com/rs/zerolog` (structured JSON logging)
- **Circuit Breaker**: `github.com/sony/gobreaker`
- **Rate Limiting**: Custom in-memory rate limiter

### Utilities
- **UUID Generation**: `github.com/google/uuid`
- **Env Loading**: `github.com/joho/godotenv`
- **Retry Logic**: `golang.org/x/time` (standard backoff)

---

## 🚀 Getting Started

### Prerequisites
- **Go 1.26.4+** ([Download](https://golang.org/dl/))
- **PostgreSQL 16+** ([Download](https://www.postgresql.org/download/))
- **Docker & Docker Compose** (optional, for containerized setup)
- **Git**

### 1️⃣ Clone Repository
```bash
git clone https://github.com/Kaipapurandeswarreddy/go-backend.git
cd go-backend
```

### 2️⃣ Set Up Environment
```bash
# Copy example environment file
cp .env.example .env

# Edit .env with your API keys and secrets
nano .env
```

### 3️⃣ Install Dependencies
```bash
go mod download
go mod tidy
```

### 4️⃣ Set Up Database

#### Option A: Using Docker Compose (Recommended)
```bash
docker-compose up -d
```

This starts PostgreSQL 16 with:
- Database: `ambigo`
- User: `ambigo`
- Password: `ambigo_dev_password`
- Port: `5432`

#### Option B: Manual PostgreSQL Setup
```bash
# Create database
createdb -U postgres ambigo

# Run migrations
goose -dir migrations postgres "postgres://ambigo:password@localhost:5432/ambigo?sslmode=disable" up
```

See [LOCAL_POSTGRES.md](LOCAL_POSTGRES.md) for detailed setup.

### 5️⃣ Run Server
```bash
# Development mode
go run ./cmd/server

# Production build
go build -o ambigo-server ./cmd/server
./ambigo-server
```

The server starts on `http://localhost:8080` (or `$PORT` if set).

### 6️⃣ Verify Installation
```bash
# Health check
curl http://localhost:8080/api/v1/health

# Prometheus metrics
curl http://localhost:8080/metrics
```

---

## ⚙️ Configuration

### Environment Variables

Copy `.env.example` → `.env` and customize:

```bash
# ========== Database ==========
DATABASE_URL=postgres://ambigo:ambigo_dev_password@localhost:5432/ambigo?sslmode=disable
MONGODB_URI=mongodb://localhost:27017/

# Pool Configuration (optional)
PG_MAX_OPEN_CONNS=25      # Default: 25
PG_MIN_OPEN_CONNS=5       # Default: 5
PG_MAX_CONN_LIFETIME=5m   # Default: 5m
PG_MAX_CONN_IDLE_TIME=2m  # Default: 2m

# ========== JWT & API Keys ==========
JWT_SECRET=<generate-with: openssl rand -hex 32>
JWT_ALGORITHM=HS256                # Signing algorithm
JWT_VALIDITY=3000000                # Token validity in ms
JWT_AUDIENCE=ambigo-app             # Optional audience claim
JWT_ISSUER=ambigo-backend           # Optional issuer claim
API_KEY=<your-api-key>

# ========== SMS (OTP) ==========
SMS_COUNTRY_KEY=<your-key>
SMS_COUNTRY_TOKEN=<your-token>
SMS_API_BASE_URL=https://restapi.smscountry.com/v0.1/Accounts/%s/SMSes/
SMS_SENDER_ID=AMBHPL
SMS_COUNTRY_CODE=91

# ========== Razorpay (Payments) ==========
RAZORPAY_KEY_ID=<your-key-id>
RAZORPAY_KEY_SECRET=<your-secret>
RAZORPAY_WEBHOOK_SECRET=<your-webhook-secret>

# ========== Zwitch (Payouts & KYC) ==========
ZWITCH_ACCOUNT_ID=<payout-account-id>
ZWITCH_VERIFICATION_ACCOUNT_ID=<kyc-account-id>
ZWITCH_KEY=<your-key>
ZWITCH_SECRET=<your-secret>
ZWITCH_WEBHOOK_SECRET=<your-webhook-secret>
WITHDRAWAL_FEE=7                    # Percentage deducted

# ========== Call Masking (Cloudshope) ==========
CLOUDSHOPE_TOKEN=<your-token>
CLOUDSHOPE_NUMBER=<virtual-number>

# ========== Firebase & Google ==========
FIREBASE_CREDENTIALS_PATH=firebase-key.json
GOOGLE_MAPS_API_KEY=<your-key>

# ========== Server ==========
PORT=8080
DEBUG=true
ENABLE_PPROF=false

# ========== Email (Resend) ==========
RESEND_API_KEY=re_...
RESEND_FROM_EMAIL=noreply@ambigo.in
```

**⚠️ Required Variables** (must be set):
- `JWT_SECRET`
- `API_KEY`
- `DATABASE_URL` or `MONGODB_URI`

**Optional but Recommended**:
- `RAZORPAY_KEY_ID`, `RAZORPAY_KEY_SECRET`
- `GOOGLE_MAPS_API_KEY`
- `FIREBASE_CREDENTIALS_PATH`

---

## 📚 API Documentation

### Authentication

All requests (except `/health`, `/metrics`, `/ws`) require:
```bash
X-API-Key: <your-api-key>
```

Protected endpoints also require JWT:
```bash
Authorization: Bearer <access_token>
```

### Request & Response Format

**Request:**
```bash
curl -X POST http://localhost:8080/api/v2/auth/user/request-otp \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"mobile":"919876543210"}'
```

**Success Response (200 OK):**
```json
{
  "data": {
    "otp_sent": true,
    "mobile": "919876543210"
  }
}
```

**Error Response (400 Bad Request):**
```json
{
  "error": "Bad Request",
  "detail": "Invalid mobile number",
  "code": 400
}
```

### Endpoint Categories

See [API_DOCUMENTATION.md](API_DOCUMENTATION.md) for complete endpoint reference:

| Category | Endpoints | Key Operations |
|----------|-----------|-----------------|
| **Health & Metrics** | 2 | Health checks, Prometheus scraping |
| **Authentication** | 8 | OTP flow, token refresh, logout |
| **Profile** | 4 | User/driver profile retrieval, FCM updates |
| **Verification** | 2 | Driver document verification |
| **Rides** | 13 | Full ride lifecycle, history, routing |
| **Payments** | 6 | Online/cash payments, Razorpay webhooks |
| **Wallet** | 4 | Balance, withdrawals, transactions |
| **Shared** | 6 | Hospitals, ambulance types, feedback |
| **Referral** | 1 | Referral rewards |
| **Admin** | 20 | Driver management, analytics |
| **Admin Extended** | 7 | User management, ride history |
| **Admin Offers** | 5 | Promotional offers, referral config |

### Usage Examples

#### 1. User Authentication
```bash
# Request OTP
curl -X POST http://localhost:8080/api/v2/auth/user/request-otp \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"mobile":"919876543210"}'

# Verify OTP
curl -X POST http://localhost:8080/api/v2/auth/user/verify-otp \
  -H "X-API-Key: your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "mobile":"919876543210",
    "otp":"123456"
  }'
# Returns: { "access_token": "...", "refresh_token": "..." }
```

#### 2. Request a Ride
```bash
curl -X POST http://localhost:8080/api/v2/rides/request \
  -H "X-API-Key: your-api-key" \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "pickup_location": {
      "lat": 28.6139,
      "lng": 77.2090,
      "address": "Delhi"
    },
    "dropoff_location": {
      "lat": 28.5355,
      "lng": 77.3910,
      "address": "Gurgaon"
    },
    "ambulance_type": "type-1"
  }'
```

#### 3. Get Current Active Ride
```bash
curl -X POST http://localhost:8080/api/v2/rides/current \
  -H "X-API-Key: your-api-key" \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json"
```

---

## 🔌 WebSocket Integration

### Connection
```javascript
// JavaScript/Flutter
const ws = new WebSocket(
  `ws://localhost:8080/ws?token=<jwt_token>&api_key=<api_key>`
);

ws.onopen = () => console.log("Connected");
ws.onmessage = (e) => console.log("Received:", JSON.parse(e.data));
ws.onerror = (e) => console.error("Error:", e);
```

### Client Events (Send to Server)
- **`LOCATION_UPDATE`**: Send driver/user location
- **`WATCH_RIDE`**: Subscribe to ride updates
- **`RIDE_DECLINED`**: Decline a ride offer
- **`PING`**: Keep-alive heartbeat

### Server Events (Receive from Server)
- **`RIDE_REQUESTED`**: New ride request broadcast
- **`RIDE_UPDATE`**: Ride status changes
- **`LOCATION_UPDATE`**: Location updates
- **`ERROR`**: Error notifications
- **`SESSION_REPLACED`**: User logged in elsewhere (session conflict)

**Configuration:**
- **Ping Interval**: 54 seconds
- **Pong Timeout**: 60 seconds
- **Max Message Size**: 1024 bytes

See [WEBSOCKET_DOCUMENTATION.md](WEBSOCKET_DOCUMENTATION.md) for complete WebSocket reference.

---

## 📦 Database Setup

### PostgreSQL Schema (Primary)

The schema includes:

```sql
-- Users & Authentication
users                  -- All users (riders/drivers)
admin_users            -- Admin portal users
auth_sessions          -- Active sessions with refresh tokens

-- Rides & Matching
rides                  -- Ride lifecycle & history
ride_offers            -- Driver-specific ride offers
ride_locations         -- Real-time location tracking

-- Payments & Wallet
payments               -- Payment records (online/cash)
wallets                -- Driver wallet balances
wallet_transactions    -- Withdrawal & deposit records

-- Verification
driver_verifications   -- Document upload history
driver_verification_status  -- Current KYC status

-- Business
referrals              -- Referral codes & rewards
promotional_offers     -- Admin-created promotions
feedback               -- Ride ratings & reviews

-- Audit & Metadata
audit_logs             -- All system changes
```

### MongoDB Collections (Legacy)

For backward compatibility during migration:

```javascript
// Collections used for dual-write
{
  "users": {...},
  "rides": {...},
  "payments": {...},
  "verifications": {...}
}
```

### Running Migrations
```bash
# Up
goose -dir migrations postgres "$DATABASE_URL" up

# Down (last 1)
goose -dir migrations postgres "$DATABASE_URL" down

# Status
goose -dir migrations postgres "$DATABASE_URL" status
```

See [LOCAL_POSTGRES.md](LOCAL_POSTGRES.md) for detailed DB setup & troubleshooting.

---

## 👨‍💻 Development

### Project Structure
```
go-backend/
├── api/
│   ├── handlers/              # HTTP request handlers
│   │   ├── admin.go           # Admin CRUD operations
│   │   ├── auth.go            # Authentication flows
│   │   ├── payment.go         # Payment processing
│   │   ├── ride.go            # Ride lifecycle
│   │   ├── profile.go         # User/driver profiles
│   │   ├── wallet.go          # Wallet management
│   │   ├── ws.go              # WebSocket upgrade
│   │   └── ...
│   ├── middleware/             # Request middleware
│   │   ├── cors.go
│   │   ├── jwt.go
│   │   ├── ratelimit.go
│   │   ├── metrics.go
│   │   └── ...
│   └── response/              # Response helpers
│
├── cmd/server/
│   └── main.go                # Server entry point & routing
│
├── config/                    # Configuration loading
├── internal/                  # Business logic
│   ├── auth/                  # JWT & session management
│   ├── ride/                  # Ride store & dispatcher
│   ├── payment/               # Payment processing
│   ├── location/              # H3 geospatial indexing
│   ├── notification/          # FCM push
│   ├── eventbus/              # In-memory pub/sub
│   ├── pricing/               # Fare calculation
│   ├── referral/              # Referral system
│   ├── admin/                 # Admin operations
│   └── ...
│
├── interfaces/                # Interface definitions
├── migrations/                # SQL migration files
├── db/                        # Database query layer (sqlc)
│
├── Dockerfile                 # Multi-stage Cloud Run image
├── docker-compose.yml         # Local dev environment
├── cloudbuild.yaml            # GCP Cloud Build config
│
├── go.mod                     # Dependency manifest
├── go.sum                     # Dependency checksums
│
├── API_DOCUMENTATION.md       # Complete API reference
├── WEBSOCKET_DOCUMENTATION.md # WebSocket protocol
├── LOCAL_POSTGRES.md          # DB setup guide
├── TESTING_REPORT.md          # Test coverage report
└── README.md                  # This file
```

### Running Tests
```bash
# All tests with verbose output
go test -v ./...

# Coverage report
go test -cover ./...

# Specific package
go test -v ./internal/ride

# Benchmark
go test -bench=. ./internal/pricing
```

See [TESTING_REPORT.md](TESTING_REPORT.md) for coverage details.

### Code Style & Linting
```bash
# Format code
go fmt ./...

# Lint (requires golangci-lint)
golangci-lint run ./...

# Vet for suspicious constructs
go vet ./...
```

### Hot Reload Development
```bash
# Install air (hot reload)
go install github.com/cosmtrek/air@latest

# Run with auto-reload on file changes
air
```

### Profiling
Enable Go pprof profiling (CPU, memory, goroutines):

```bash
# In .env
ENABLE_PPROF=true

# Access profiles (behind X-API-Key guard)
curl -H "X-API-Key: your-key" http://localhost:8080/debug/pprof/
```

---

## 🐳 Deployment

### Docker Build & Run

#### Local Testing
```bash
# Build image
docker build -t ambigo-backend:latest .

# Run container
docker run -p 8080:8080 \
  -e DATABASE_URL="postgres://..." \
  -e JWT_SECRET="your-secret" \
  -e API_KEY="your-key" \
  ambigo-backend:latest
```

#### Cloud Run (GCP)
```bash
# Deploy from current branch
gcloud run deploy ambigo-backend \
  --source . \
  --region us-central1 \
  --set-env-vars DATABASE_URL=postgres://... \
  --set-secrets JWT_SECRET=projects/PROJECT_ID/secrets/jwt-secret/versions/latest
```

Uses `cloudbuild.yaml` for automated GCP Cloud Build:
- Multi-stage builds (builder → runtime)
- Alpine 3.21 minimal image (~15 MB)
- Non-root execution (UID 65534)
- CGO support for H3 geospatial library

### Production Checklist
- [ ] Set strong `JWT_SECRET` (generate: `openssl rand -hex 32`)
- [ ] Set strong `API_KEY`
- [ ] Configure PostgreSQL with backup strategy
- [ ] Set up Firebase Service Account JSON
- [ ] Configure API keys for Google Maps, Razorpay, SMS, etc.
- [ ] Enable HTTPS/TLS in reverse proxy (Nginx/Cloud Run)
- [ ] Set up monitoring with Prometheus scrape target
- [ ] Configure log aggregation (Stackdriver/CloudLogging)
- [ ] Set up alerting for critical endpoints
- [ ] Test WebSocket connectivity
- [ ] Test payment webhooks (Razorpay, Zwitch)
- [ ] Test SMS delivery (SMS Country)
- [ ] Test FCM push notifications
- [ ] Configure rate limits per environment
- [ ] Enable pprof only in staging, disable in production

---

## 📊 Monitoring & Observability

### Metrics Endpoint
```bash
curl http://localhost:8080/metrics
```

Exposed Prometheus metrics:
- HTTP request counts & latency (bucketed)
- Database connection pool stats
- WebSocket connection counts
- Business metrics (rides, payments, etc.)

### Health Check
```bash
curl http://localhost:8080/api/v1/health
```

Returns:
```json
{
  "status": "healthy",
  "database": "connected",
  "firebase": "available",
  "timestamp": "2024-09-28T10:30:00Z"
}
```

### Logging
Structured logging via `zerolog`:
```json
{
  "level": "info",
  "timestamp": "2024-09-28T10:30:00Z",
  "request_id": "uuid-...",
  "endpoint": "/api/v2/rides/request",
  "method": "POST",
  "status": 200,
  "latency_ms": 245,
  "user_id": "user-123"
}
```

---

## 🐛 Troubleshooting

### Server Won't Start
**Check prerequisites:**
```bash
go version                      # Go 1.26.4+
psql --version                  # PostgreSQL 16+
docker --version                # If using Docker
```

### Database Connection Errors
```bash
# Test PostgreSQL connection
psql -h localhost -U ambigo -d ambigo

# Check connection string in .env
# Format: postgres://user:password@host:port/dbname?sslmode=disable
```

### JWT/Authentication Issues
```bash
# Verify JWT_SECRET is set
echo $JWT_SECRET

# Generate new secret if needed
openssl rand -hex 32
```

### WebSocket Connection Issues
- Ensure WebSocket upgrade path is `/ws`
- Include both `token` and `api_key` query parameters
- Check firewall allows WebSocket connections

### Payment Webhook Failures
- Verify `RAZORPAY_WEBHOOK_SECRET` matches Razorpay dashboard
- Check webhook URL is publicly accessible
- Test with Razorpay webhook simulator

---

## 📄 Documentation

- **[API_DOCUMENTATION.md](API_DOCUMENTATION.md)** — Complete REST API reference (63 endpoints)
- **[WEBSOCKET_DOCUMENTATION.md](WEBSOCKET_DOCUMENTATION.md)** — WebSocket protocol & events
- **[LOCAL_POSTGRES.md](LOCAL_POSTGRES.md)** — PostgreSQL setup & migration guide
- **[TESTING_REPORT.md](TESTING_REPORT.md)** — Test coverage & benchmarks
- **[SystemOverview.drawio](SystemOverview.drawio)** — Architecture diagram (draw.io format)

---

## 🤝 Contributing

Contributions are welcome! Please follow these steps:

1. **Fork the repository**
```bash
git clone https://github.com/your-username/go-backend.git
cd go-backend
```

2. **Create a feature branch**
```bash
git checkout -b feature/your-feature-name
```

3. **Make changes & commit**
```bash
git add .
git commit -m "feat: description of changes"
```

4. **Push & create Pull Request**
```bash
git push origin feature/your-feature-name
```

**Code Guidelines:**
- Follow Go conventions ([Effective Go](https://golang.org/doc/effective_go))
- Write unit tests for new functions
- Run `go fmt` and `go vet` before committing
- Keep commits atomic and well-described

---

## 📞 Support & Contact

- **Issues**: [GitHub Issues](https://github.com/Kaipapurandeswarreddy/go-backend/issues)
- **Discussions**: [GitHub Discussions](https://github.com/Kaipapurandeswarreddy/go-backend/discussions)
- **Email**: ambigo-support@example.com
- **Documentation**: See docs folder in repository

---

## 📜 License

This project is licensed under the **MIT License** — see [LICENSE](LICENSE) file for details.

---

## 🚀 Quick Links

- **Repository**: https://github.com/Kaipapurandeswarreddy/go-backend
- **Postman Collection**: [postman_collection.json](postman_collection.json)
- **WebSocket Postman**: [websocket_collection.json](websocket_collection.json)
- **Environment Config**: [.env.example](.env.example)

---

**Built with ❤️ by the Ambigo Team | Go 1.26.4 | PostgreSQL 16 | Docker Ready**
