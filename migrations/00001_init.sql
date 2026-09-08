-- +goose Up
-- Ambigo initial schema — PostgreSQL, UUID v4 PKs, local dev + Neon compatible
-- Replaces MongoDB 4 DBs (Users, Rides, Records, Data) with one Postgres DB, flat public schema.

-- Extensions
CREATE EXTENSION IF NOT EXISTS "pgcrypto"; -- gen_random_uuid()

-- =====================================================================
-- USERS domain (was Mongo Users DB) — no FK deps
-- =====================================================================

CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    mobile          TEXT NOT NULL UNIQUE,
    referral_code   TEXT NOT NULL DEFAULT '',
    my_referral_code TEXT,
    location        JSONB,
    fcm_token       TEXT,
    jwt_token       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_my_referral_code_unique ON users(my_referral_code) WHERE my_referral_code IS NOT NULL AND my_referral_code <> '';

CREATE TABLE drivers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL,
    mobile              TEXT NOT NULL UNIQUE,
    photo               TEXT NOT NULL DEFAULT '',
    vehicle_type        TEXT NOT NULL,
    vehicle_registration TEXT NOT NULL,
    wallet_details      JSONB NOT NULL DEFAULT '{}'::jsonb,
    wallet_balance      NUMERIC(12,2) NOT NULL DEFAULT 0 CHECK (wallet_balance >= -1000000),
    referral_code       TEXT NOT NULL DEFAULT '',
    my_referral_code    TEXT,
    location            JSONB,
    fcm_token           TEXT,
    jwt_token           TEXT,
    last_location_update TIMESTAMPTZ,
    details             JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX drivers_my_referral_code_unique ON drivers(my_referral_code) WHERE my_referral_code IS NOT NULL AND my_referral_code <> '';

CREATE TABLE unverified_drivers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    mobile          TEXT NOT NULL,
    portrait_image  TEXT NOT NULL DEFAULT '',
    poi_image       TEXT NOT NULL DEFAULT '',
    dl_image        TEXT NOT NULL DEFAULT '',
    rc_image        TEXT NOT NULL DEFAULT '',
    amb_front       TEXT NOT NULL DEFAULT '',
    amb_inside      TEXT NOT NULL DEFAULT '',
    vehicle_type    TEXT NOT NULL DEFAULT '',
    under_progress  BOOLEAN NOT NULL DEFAULT false,
    error_message   TEXT,
    fcm_token       TEXT,
    jwt_token       TEXT,
    location        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX unverified_drivers_mobile_idx ON unverified_drivers(mobile);
CREATE INDEX unverified_drivers_under_progress_idx ON unverified_drivers(under_progress) WHERE under_progress = true;

CREATE TABLE admins (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username        TEXT UNIQUE,
    hashed_password TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('super_admin','admin','')),
    active          BOOLEAN NOT NULL DEFAULT true,
    mobile          TEXT,
    fcm_token       TEXT,
    location        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE auth_otp (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    number      TEXT NOT NULL,
    otp         TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX auth_otp_number_idx ON auth_otp(number);
CREATE INDEX auth_otp_created_at_idx ON auth_otp(created_at);

CREATE TABLE otp_attempts (
    mobile       TEXT PRIMARY KEY,
    attempts     INT NOT NULL DEFAULT 0,
    locked_until TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- =====================================================================
-- DATA domain — reference data (no FK, must come before hospital_mds)
-- =====================================================================

CREATE TABLE ambulance_types (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL,
    photo               TEXT NOT NULL DEFAULT '',
    helper_included     BOOLEAN NOT NULL DEFAULT false,
    otp_required        BOOLEAN NOT NULL DEFAULT false,
    listing_threshold   DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (listing_threshold >= 0),
    base_fare           DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (base_fare >= 0),
    driver_share        DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (driver_share >= 0),
    pricing_tier        JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(pricing_tier) = 'array'),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE hospitals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            JSONB NOT NULL,
    address         JSONB NOT NULL,
    city            JSONB NOT NULL,
    location        JSONB NOT NULL,
    location_lng    DOUBLE PRECISION GENERATED ALWAYS AS ((location->'coordinates'->>0)::double precision) STORED,
    location_lat    DOUBLE PRECISION GENERATED ALWAYS AS ((location->'coordinates'->>1)::double precision) STORED,
    timing          JSONB,
    always_open     BOOLEAN NOT NULL DEFAULT false,
    services        TEXT[] NOT NULL DEFAULT '{}',
    place_id        TEXT UNIQUE,
    h3_cells        TEXT[] NOT NULL DEFAULT '{}',
    source          TEXT NOT NULL DEFAULT 'admin' CHECK (source IN ('admin','google')),
    fetched_at      TIMESTAMPTZ,
    hospital_type   TEXT CHECK (hospital_type IN ('government','multi_speciality','private','clinic','general')),
    category        TEXT CHECK (category IN ('emergency','non_emergency')),
    google_types    TEXT[] NOT NULL DEFAULT '{}',
    type_locked     BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX hospitals_h3_cells_gin ON hospitals USING GIN (h3_cells);
CREATE INDEX hospitals_category_idx ON hospitals(category) WHERE category IS NOT NULL;

CREATE TABLE hospital_cities (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    lat             DOUBLE PRECISION NOT NULL,
    lng             DOUBLE PRECISION NOT NULL,
    radius_m        BIGINT NOT NULL,
    last_fetched    TIMESTAMPTZ,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX hospital_cities_enabled_idx ON hospital_cities(enabled) WHERE enabled = true;

CREATE TABLE pending_hospitals (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL,
    address             TEXT NOT NULL,
    email               TEXT NOT NULL DEFAULT '',
    md_number           TEXT NOT NULL,
    official_number     TEXT NOT NULL DEFAULT '',
    city                TEXT NOT NULL,
    location            JSONB,
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    rejection_reason    TEXT,
    md_id               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at         TIMESTAMPTZ,
    reviewed_by         TEXT
);
CREATE INDEX pending_hospitals_status_idx ON pending_hospitals(status);
CREATE INDEX pending_hospitals_md_number_idx ON pending_hospitals(md_number);

CREATE TABLE counters (
    id      TEXT PRIMARY KEY,
    value   INT NOT NULL DEFAULT 0
);

CREATE TABLE referral_configs (
    type                TEXT PRIMARY KEY,
    referrer_amount     NUMERIC(10,2) NOT NULL DEFAULT 0,
    new_user_amount     NUMERIC(10,2) NOT NULL DEFAULT 0,
    rides_required      INT NOT NULL DEFAULT 0,
    enabled             BOOLEAN NOT NULL DEFAULT true,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE offers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    description         TEXT NOT NULL,
    user_id             TEXT,
    offer_percentage    NUMERIC(5,2),
    offer_amount        NUMERIC(10,2),
    max_discount        NUMERIC(10,2),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX offers_user_id_idx ON offers(user_id) WHERE user_id IS NOT NULL;

-- Hospital domain — depends on hospitals / pending_hospitals / drivers
CREATE TABLE hospital_mds (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hospital_pending_id UUID REFERENCES pending_hospitals(id) ON DELETE SET NULL,
    hospital_id         UUID REFERENCES hospitals(id) ON DELETE SET NULL,
    name                TEXT NOT NULL,
    email               TEXT NOT NULL DEFAULT '',
    mobile              TEXT NOT NULL UNIQUE,
    official_number     TEXT NOT NULL DEFAULT '',
    username            TEXT UNIQUE,
    password_hash       TEXT,
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','rejected','banned')),
    jwt_token           TEXT,
    fcm_token           TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX hospital_mds_hospital_id_idx ON hospital_mds(hospital_id) WHERE hospital_id IS NOT NULL;

CREATE TABLE hospital_receptionists (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hospital_id     UUID NOT NULL REFERENCES hospitals(id) ON DELETE CASCADE,
    created_by_md_id UUID NOT NULL REFERENCES hospital_mds(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    username        TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    mobile          TEXT,
    active          BOOLEAN NOT NULL DEFAULT true,
    jwt_token       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX hospital_receptionists_hospital_id_idx ON hospital_receptionists(hospital_id);

CREATE TABLE ambulance_attendants (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL,
    mobile              TEXT NOT NULL UNIQUE,
    assigned_driver_id  UUID REFERENCES drivers(id) ON DELETE SET NULL,
    jwt_token           TEXT,
    fcm_token           TEXT,
    active              BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ambulance_attendants_assigned_driver_id_idx ON ambulance_attendants(assigned_driver_id) WHERE assigned_driver_id IS NOT NULL;

-- =====================================================================
-- RECORDS domain — payments etc. (offers already created)
-- =====================================================================

CREATE TABLE payments (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             TEXT NOT NULL,
    partner_id          TEXT NOT NULL,
    ride_id             TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    original_amount     NUMERIC(10,2) NOT NULL,
    charged_amount      NUMERIC(10,2) NOT NULL,
    driver_share        NUMERIC(10,2) NOT NULL,
    payment_mode        TEXT NOT NULL CHECK (payment_mode IN ('cash','online')),
    paid                BOOLEAN NOT NULL DEFAULT false,
    razorpay_order_id   TEXT UNIQUE,
    razorpay_payment_id TEXT,
    paid_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    offer               UUID REFERENCES offers(id) ON DELETE SET NULL
);
CREATE INDEX payments_user_paid_idx ON payments(user_id, paid);
CREATE INDEX payments_partner_paid_idx ON payments(partner_id, paid);
CREATE INDEX payments_ride_id_idx ON payments(ride_id);

-- =====================================================================
-- RIDES domain — depends on users, drivers, ambulance_types, hospitals, payments
-- =====================================================================

CREATE TABLE rides (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id),
    driver_id           UUID REFERENCES drivers(id),
    amb_type_id         UUID REFERENCES ambulance_types(id),
    hospital_id         UUID REFERENCES hospitals(id),
    start_otp           TEXT,
    status              TEXT NOT NULL CHECK (status IN ('SEARCHING','ASSIGNED','ARRIVED','IN_PROGRESS','COMPLETED','CANCELLED')),
    pickup              JSONB NOT NULL,
    pickup_address      TEXT NOT NULL DEFAULT '',
    pickup_h3_cell      TEXT NOT NULL DEFAULT '',
    drop                JSONB NOT NULL,
    drop_address        TEXT NOT NULL DEFAULT '',
    route_distance_km       DOUBLE PRECISION,
    route_duration_seconds  INT,
    route_polyline          TEXT,
    fare_base               NUMERIC(10,2),
    fare_distance           NUMERIC(10,2),
    fare_emergency          NUMERIC(10,2),
    fare_night              NUMERIC(10,2),
    fare_waiting            NUMERIC(10,2),
    fare_total              NUMERIC(10,2),
    fare_driver_share       NUMERIC(10,2),
    fare_referral_discount  NUMERIC(10,2),
    fare_currency           TEXT NOT NULL DEFAULT 'INR',
    emergency_type      TEXT,
    emergency_priority  INT NOT NULL DEFAULT 0,
    payment_mode        TEXT NOT NULL CHECK (payment_mode IN ('cash','online')),
    payment_id          UUID REFERENCES payments(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    assigned_at         TIMESTAMPTZ,
    arrived_at          TIMESTAMPTZ,
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    dispatch_candidates_searched  INT NOT NULL DEFAULT 0,
    dispatch_offers_sent          INT NOT NULL DEFAULT 0,
    dispatch_offers_declined      INT NOT NULL DEFAULT 0,
    dispatch_offers_timed_out     INT NOT NULL DEFAULT 0,
    dispatch_assignment_latency_ms INT NOT NULL DEFAULT 0,
    cancellation_reason TEXT,
    available_types     JSONB,
    latest_condition    JSONB,
    condition_on_arrival JSONB
);
CREATE INDEX rides_user_created_idx ON rides(user_id, created_at DESC);
CREATE INDEX rides_driver_created_idx ON rides(driver_id, created_at DESC) WHERE driver_id IS NOT NULL;
CREATE INDEX rides_status_created_idx ON rides(status, created_at DESC);
CREATE INDEX rides_hospital_idx ON rides(hospital_id) WHERE hospital_id IS NOT NULL;
CREATE INDEX rides_status_searching_created_idx ON rides(created_at) WHERE status = 'SEARCHING';

CREATE TABLE ride_condition_updates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id     UUID NOT NULL REFERENCES rides(id) ON DELETE CASCADE,
    level       TEXT NOT NULL,
    severity    INT NOT NULL,
    note        TEXT,
    source      TEXT NOT NULL CHECK (source IN ('user','attendant')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ride_condition_updates_ride_id_idx ON ride_condition_updates(ride_id, created_at);

-- =====================================================================
-- Remaining RECORDS (no FK deps)
-- =====================================================================

CREATE TABLE refresh_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         TEXT NOT NULL,
    role            TEXT NOT NULL,
    token_hash      TEXT NOT NULL UNIQUE,
    session_id      TEXT,
    device_id       TEXT,
    device_name     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked         BOOLEAN NOT NULL DEFAULT false,
    revoked_at      TIMESTAMPTZ,
    revoked_reason  TEXT,
    superseded_by   UUID REFERENCES refresh_tokens(id) ON DELETE SET NULL
);
CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens(user_id);
CREATE INDEX refresh_tokens_user_revoked_idx ON refresh_tokens(user_id, revoked) WHERE revoked = false;

CREATE TABLE referrals_legacy (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_type       TEXT NOT NULL DEFAULT '',
    ref_from        TEXT NOT NULL DEFAULT '',
    ref_to          TEXT NOT NULL DEFAULT '',
    value           TEXT NOT NULL DEFAULT '',
    rides_done      INT NOT NULL DEFAULT 0,
    amount_recieved BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE wallet_transactions (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id               TEXT NOT NULL,
    zwitch_beneficiary_id   TEXT NOT NULL DEFAULT '',
    zwitch_id               TEXT NOT NULL DEFAULT '',
    amount                  NUMERIC(10,2) NOT NULL,
    account_no              TEXT NOT NULL DEFAULT '',
    merchant_reference_id   TEXT UNIQUE,
    bank_reference_no       TEXT NOT NULL DEFAULT '',
    zwitch_transfer_id      TEXT NOT NULL DEFAULT '',
    status                  TEXT NOT NULL DEFAULT 'pending',
    error_message           TEXT NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ
);
CREATE INDEX wallet_transactions_driver_id_idx ON wallet_transactions(driver_id);

CREATE TABLE feedback (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     TEXT NOT NULL,
    driver_id   TEXT NOT NULL,
    ride_id     TEXT NOT NULL,
    rating      DOUBLE PRECISION NOT NULL CHECK (rating >= 1 AND rating <= 5),
    content     TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX feedback_driver_id_idx ON feedback(driver_id);

CREATE TABLE audit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type  TEXT NOT NULL,
    channel     TEXT NOT NULL,
    payload     TEXT NOT NULL,
    request_id  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_channel_idx ON audit_log(channel);
CREATE INDEX audit_log_created_at_idx ON audit_log(created_at);
CREATE INDEX audit_log_request_id_idx ON audit_log(request_id) WHERE request_id IS NOT NULL;

CREATE TABLE referral_records (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type                TEXT NOT NULL,
    referrer_id         TEXT NOT NULL,
    referrer_role       TEXT NOT NULL CHECK (referrer_role IN ('user','driver')),
    referee_id          TEXT NOT NULL,
    referee_role        TEXT NOT NULL CHECK (referee_role IN ('user','driver')),
    code                TEXT NOT NULL,
    rides_required      INT NOT NULL,
    rides_done          INT NOT NULL DEFAULT 0,
    referrer_credited   BOOLEAN NOT NULL DEFAULT false,
    referee_credited    BOOLEAN NOT NULL DEFAULT false,
    referrer_amount     NUMERIC(10,2) NOT NULL DEFAULT 0,
    referee_amount      NUMERIC(10,2) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ,
    UNIQUE (referrer_id, referee_id, type)
);
CREATE INDEX referral_records_referrer_idx ON referral_records(referrer_id);
CREATE INDEX referral_records_referee_idx ON referral_records(referee_id);

-- Goose migrations table is created automatically by goose

-- +goose Down
DROP TABLE IF EXISTS referral_records;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS feedback;
DROP TABLE IF EXISTS wallet_transactions;
DROP TABLE IF EXISTS referrals_legacy;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS ride_condition_updates;
DROP TABLE IF EXISTS rides;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS offers;
DROP TABLE IF EXISTS referral_configs;
DROP TABLE IF EXISTS counters;
DROP TABLE IF EXISTS pending_hospitals;
DROP TABLE IF EXISTS hospital_cities;
DROP TABLE IF EXISTS hospitals;
DROP TABLE IF EXISTS ambulance_types;
DROP TABLE IF EXISTS ambulance_attendants;
DROP TABLE IF EXISTS hospital_receptionists;
DROP TABLE IF EXISTS hospital_mds;
DROP TABLE IF EXISTS otp_attempts;
DROP TABLE IF EXISTS auth_otp;
DROP TABLE IF EXISTS admins;
DROP TABLE IF EXISTS unverified_drivers;
DROP TABLE IF EXISTS drivers;
DROP TABLE IF EXISTS users;
