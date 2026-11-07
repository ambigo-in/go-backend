# Ambigo V2 — API Documentation

## Overview

Ambigo is a Go-based ambulance booking backend. It exposes **63 REST API endpoints** and **1 WebSocket endpoint** across 14 functional modules.

| Metric | Count |
|--------|-------|
| Total REST Endpoints | 63 |
| Total WebSocket Endpoints | 1 |
| Modules | 14 |
| Databases | 4 (Users, Rides, Records, Data) |

---

## Architecture

```
┌──────────────┐     ┌────────────────────┐     ┌─────────────┐
│  Flutter App  │────▶│   Go HTTP Server   │────▶│  MongoDB     │
│  (User/Driver)│     │   net/http Mux      │     │  4 databases │
│               │◀───▶│   WebSocket Hub     │     └─────────────┘
└──────────────┘     └────────────────────┘
                              │
                     ┌────────┴────────┐
                     │   EventBus      │
                     │  (In-Memory)    │
                     └────────┬────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
         WebSocket       FCM Push        Audit Log
         Notifier        Notifier        Persistence
```

---

## Authentication

### API Key (Global)
All requests (except `/metrics`, `/health`, `/ws`, `/webhook/razorpay`) require the `X-API-Key` header.

```
X-API-Key: <your-api-key>
```

### JWT Bearer Token (Protected Routes)
Protected endpoints require a JWT in the `Authorization` header:

```
Authorization: Bearer <access_token>
```

### Roles
| Role | Description | Login Method |
|------|-------------|--------------|
| `user` | Ride requesters | OTP (mobile) |
| `driver` | Verified ambulance drivers | OTP (mobile) |
| `unvrf_driver` | Unverified (pending approval) drivers | OTP (mobile) |
| `admin` | Admin portal users | Username/password or OTP |

### Token Lifecycle
- **Access Token**: Short-lived JWT (configurable via `JWT_VALIDITY`)
- **Refresh Token**: Long-lived, stored in MongoDB, supports rotation with chain lineage
- **Refresh Flow**: `POST /api/v2/auth/refresh` with `refresh_token` body parameter

---

## Required Headers

| Header | Value | Scope |
|--------|-------|-------|
| `X-API-Key` | `<api_key>` | All endpoints (except health/metrics/ws/webhook) |
| `Authorization` | `Bearer <token>` | Protected endpoints |
| `Content-Type` | `application/json` | All POST requests with body |

---

## Global Middleware Stack

Applied to all requests in order:
1. **CORS** — Permissive for mobile apps
2. **RequestID** — Generates unique request ID
3. **Metrics** — Prometheus metric collection
4. **BodyLimit** — Request body size limiting
5. **Rate Limiting** — Per-IP global limiter (100 req / 200s)
6. **API Key Auth** — Validates `X-API-Key` header

---

## Error Response Format

All errors use a consistent JSON format:

```json
{
  "error": "Bad Request",
  "detail": "Invalid mobile number",
  "code": 400
}
```

---

## Module-wise API Summary

### 1. Health & Metrics (2 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/v1/health` | None | Health check with MongoDB/Google status |
| GET | `/metrics` | None | Prometheus metrics scraping endpoint |

---

### 2. Auth — User (2 endpoints)

| Method | Endpoint | Auth | Rate Limit | Description |
|--------|----------|------|------------|-------------|
| POST | `/api/v2/auth/user/request-otp` | API Key | 3/5s (IP) | Send OTP to user mobile |
| POST | `/api/v2/auth/user/verify-otp` | API Key | 3/5s (Mobile) | Verify OTP, return tokens |

---

### 3. Auth — Driver (2 endpoints)

| Method | Endpoint | Auth | Rate Limit | Description |
|--------|----------|------|------------|-------------|
| POST | `/api/v2/auth/driver/request-otp` | API Key | 3/5s (IP) | Send OTP to driver mobile |
| POST | `/api/v2/auth/driver/verify-otp` | API Key | 3/5s (Mobile) | Verify OTP, return tokens |

---

### 4. Auth — Common (4 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/auth/refresh` | API Key | Rotate refresh token |
| POST | `/api/v2/auth/logout` | JWT (any) | Revoke all sessions |
| POST | `/api/v2/auth/sessions` | JWT (any) | List active sessions |
| POST | `/api/v2/auth/sessions/revoke` | JWT (any) | Revoke specific session |

---

### 5. Profile (4 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/user/profile` | JWT (user) | Get user profile |
| POST | `/api/v2/user/fcm` | JWT (user) | Update user FCM token |
| POST | `/api/v2/driver/profile` | JWT (driver/unvrf) | Get driver profile |
| POST | `/api/v2/driver/fcm` | JWT (driver/unvrf) | Update driver FCM token |

---

### 6. Verification (2 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/driver/verification/check` | JWT (any) | Check if driver is verified |
| POST | `/api/v2/driver/verification/update` | JWT (unvrf_driver) | Upload verification documents |

---

### 7. Rides (13 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/rides/request` | JWT (user) | Request new ride |
| POST | `/api/v2/rides/{id}/accept` | JWT (driver) | Accept ride offer |
| POST | `/api/v2/rides/{id}/arrive` | JWT (driver) | Mark arrival at pickup |
| POST | `/api/v2/rides/{id}/start` | JWT (driver) | Start ride (OTP verify) |
| POST | `/api/v2/rides/{id}/complete` | JWT (driver) | Complete ride |
| POST | `/api/v2/rides/{id}/cancel` | JWT (any) | Cancel ride |
| POST | `/api/v2/rides/history` | JWT (any) | Get ride history |
| POST | `/api/v2/rides/current` | JWT (any) | Get current active ride |
| POST | `/api/v2/rides/driver/details` | JWT (any) | Get driver info for ride |
| POST | `/api/v2/rides/user/details` | JWT (any) | Get user info for ride |
| POST | `/api/v2/rides/feedback/list` | JWT (driver) | List driver's feedback |
| POST | `/api/v2/route` | JWT (any) | Route preview |
| POST | `/api/v2/fare/estimate` | JWT (any) | Fare estimate |

---

### 8. Payment (6 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/payment/pending` | JWT (any) | Get pending payment |
| POST | `/api/v2/payment/ride` | JWT (any) | Get payment by ride ID |
| POST | `/api/v2/payment/user/process` | JWT (user) | Process online payment |
| POST | `/api/v2/payment/user/process-cash` | JWT (user) | Switch to cash payment |
| POST | `/api/v2/payment/driver/process` | JWT (driver) | Confirm cash collection |
| POST | `/api/v2/payment/webhook/razorpay` | HMAC Sig | Razorpay webhook |

---

### 9. Wallet (4 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/driver/wallet/get` | JWT (driver) | Get wallet details |
| POST | `/api/v2/driver/wallet/update` | JWT (driver) | Update bank details |
| POST | `/api/v2/driver/wallet/withdraw` | JWT (driver) | Initiate withdrawal |
| POST | `/api/v2/driver/wallet/transactions/list` | JWT (driver) | List transactions |

---

### 10. Shared (6 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/shared/call/mask` | JWT (any) | Masked call via Cloudshope |
| POST | `/api/v2/shared/updates/ambulance_types/check` | API Key | Ambulance type counter |
| POST | `/api/v2/shared/ambulance/types/list` | API Key | List ambulance types |
| POST | `/api/v2/shared/updates/hospitals/check` | API Key | Hospital counter |
| POST | `/api/v2/shared/hospitals/list` | API Key | List hospitals |
| POST | `/api/v2/shared/feedback/submit` | JWT (any) | Submit ride feedback |

---

### 11. Referral (1 endpoint)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/referral/rewards` | JWT (any) | Get referral rewards |

---

### 12. Admin (20 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/admin/login` | API Key | Admin username/password login |
| POST | `/api/v2/admin/login/mobile` | API Key | Admin OTP request |
| POST | `/api/v2/admin/login/mobile/verify` | API Key | Admin OTP verify |
| POST | `/api/v2/admin/password` | JWT (admin) | Change admin password |
| POST | `/api/v2/admin/ambulance_types` | JWT (admin) | Create ambulance type |
| GET | `/api/v2/admin/ambulance_types` | JWT (admin) | List ambulance types |
| DELETE | `/api/v2/admin/ambulance_types/{id}` | JWT (admin) | Delete ambulance type |
| POST | `/api/v2/admin/ambulance/types/update` | JWT (admin) | Update ambulance type |
| POST | `/api/v2/admin/drivers/list` | JWT (admin) | List verified drivers |
| POST | `/api/v2/admin/drivers/details` | JWT (admin) | Get driver details |
| POST | `/api/v2/admin/drivers/add` | JWT (admin) | Add verified driver |
| POST | `/api/v2/admin/drivers/update` | JWT (admin) | Update driver |
| POST | `/api/v2/admin/drivers/delete` | JWT (admin) | Delete driver |
| POST | `/api/v2/admin/drivers/unverified/list` | JWT (admin) | List pending drivers |
| POST | `/api/v2/admin/drivers/unverified/list/all` | JWT (admin) | List all unverified |
| POST | `/api/v2/admin/drivers/unverified/fetch` | JWT (admin) | Fetch unverified driver |
| POST | `/api/v2/admin/drivers/unverified/accept` | JWT (admin+role) | Approve driver |
| POST | `/api/v2/admin/drivers/unverified/reject` | JWT (admin+role) | Reject driver |
| POST | `/api/v2/admin/drivers/unverified/counter` | JWT (admin) | Unverified count |
| POST | `/api/v2/admin/drivers/rides/list` | JWT (admin) | Driver ride history |

---

### 13. Admin — Profile, Users, Rides (7 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/admin/profile/fcm` | JWT (admin) | Update admin FCM |
| POST | `/api/v2/admin/profile/location` | JWT (admin) | Update admin location |
| POST | `/api/v2/admin/users/list` | JWT (admin) | List all users |
| POST | `/api/v2/admin/rides/user/list` | JWT (admin) | User ride history |
| POST | `/api/v2/admin/rides/completed/list` | JWT (admin) | List completed rides |
| POST | `/api/v2/admin/rides/ongoing/list` | JWT (admin) | List ongoing rides |
| POST | `/api/v2/admin/feedback/list` | JWT (admin) | List all feedback |

---

### 14. Admin — Offers & Referral (5 endpoints)

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/v2/admin/offers` | JWT (admin) | Create offer |
| GET | `/api/v2/admin/offers` | JWT (admin) | List offers |
| DELETE | `/api/v2/admin/offers/{id}` | JWT (admin) | Delete offer |
| GET | `/api/v2/admin/referral/config` | JWT (admin) | Get referral config |
| POST | `/api/v2/admin/referral/config` | JWT (admin) | Save referral config |

---

## WebSocket Summary

| Property | Value |
|----------|-------|
| Endpoint | `GET /ws` |
| Auth | Query params: `token` + `api_key` |
| Client Events | `LOCATION_UPDATE`, `WATCH_RIDE`, `RIDE_DECLINED`, `PING` |
| Server Events | `RIDE_REQUESTED`, `RIDE_UPDATE`, `LOCATION_UPDATE`, `ERROR`, `SESSION_REPLACED` |
| Ping Interval | 54 seconds |
| Pong Timeout | 60 seconds |
| Max Message Size | 1024 bytes |

See [WEBSOCKET_DOCUMENTATION.md](WEBSOCKET_DOCUMENTATION.md) for complete WebSocket details.

---

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MONGODB_URI` | No | `mongodb://localhost:27017/` | MongoDB connection string |
| `JWT_SECRET` | **Yes** | — | HMAC secret for JWT signing |
| `JWT_ALGORITHM` | No | `HS256` | JWT signing algorithm |
| `JWT_VALIDITY` | No | `3000000` | Token validity in milliseconds |
| `JWT_AUDIENCE` | No | — | JWT audience claim |
| `JWT_ISSUER` | No | — | JWT issuer claim |
| `API_KEY` | **Yes** | — | Application API key |
| `PORT` | No | `8080` | Server port |
| `GOOGLE_MAPS_API_KEY` | No | — | Google Maps/Routes API key |
| `SMS_COUNTRY_KEY` | No | — | SMS Country API key |
| `SMS_COUNTRY_TOKEN` | No | — | SMS Country API token |
| `RAZORPAY_KEY_ID` | No | — | Razorpay key ID |
| `RAZORPAY_KEY_SECRET` | No | — | Razorpay key secret |
| `RAZORPAY_WEBHOOK_SECRET` | No | — | Razorpay webhook HMAC secret |
| `ZWITCH_KEY` | No | — | Zwitch payout key |
| `ZWITCH_SECRET` | No | — | Zwitch payout secret |
| `ZWITCH_ACCOUNT_ID` | No | — | Zwitch account ID |
| `CLOUDSHOPE_TOKEN` | No | — | Cloudshope call masking token |
| `CLOUDSHOPE_NUMBER` | No | — | Cloudshope virtual number |
| `FIREBASE_CREDENTIALS_PATH` | No | — | Firebase service account JSON path |
| `ALLOW_STALE_REFRESH_CHAIN` | No | `false` | Enable stale refresh token recovery |
| `BANK_VERIFICATION_BYPASS` | No | `false` | Skip Zwitch bank verification + first-payout gate (unapproved verification product) |

---

## Folder Structure

```
ambigo-backend/
├── api/
│   ├── handlers/          # HTTP request handlers (12 files)
│   │   ├── admin.go       # Admin CRUD (20 endpoints)
│   │   ├── auth.go        # User/Driver OTP auth (8 endpoints)
│   │   ├── feedback.go    # Feedback submission
│   │   ├── offer.go       # Promotional offers
│   │   ├── payment.go     # Razorpay/cash payments
│   │   ├── profile.go     # User/Driver profile
│   │   ├── referral.go    # Referral rewards
│   │   ├── ride.go        # Full ride lifecycle
│   │   ├── shared.go      # Shared endpoints
│   │   ├── verification.go# Driver doc verification
│   │   ├── wallet.go      # Driver wallet
│   │   └── ws.go          # WebSocket upgrade handler
│   ├── middleware/         # Request middleware
│   │   ├── bodylimit.go   # Body size limiter
│   │   ├── cors.go        # CORS headers
│   │   ├── jwt.go         # JWT + Role auth
│   │   ├── metrics.go     # Prometheus metrics
│   │   ├── ratelimit.go   # IP/mobile rate limiters
│   │   └── requestid.go   # Request ID generation
│   └── response/          # Response helpers
├── cmd/server/main.go     # Server bootstrap + route registration
├── config/                # Configuration loading
├── internal/              # Business logic
│   ├── admin/             # Admin stores
│   ├── auth/              # Auth stores + JWT
│   ├── dispatch/          # Ride dispatcher + matching
│   ├── eventbus/          # In-memory pub/sub
│   ├── location/          # H3 spatial driver store
│   ├── notification/      # FCM push notifications
│   ├── offer/             # Offer store
│   ├── payment/           # Payment + wallet stores
│   ├── pricing/           # Fare calculation engine
│   ├── referral/          # Referral system
│   ├── ride/              # Ride store + models
│   ├── telephony/         # Call masking (Cloudshope)
│   ├── translation/       # Google Translate
│   └── websocket/         # WebSocket hub + notifier
└── interfaces/            # Interface definitions
```
