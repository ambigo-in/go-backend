package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ambigo-backend/api/handlers"
	"ambigo-backend/api/middleware"
	"ambigo-backend/config"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/dispatch"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/hospital"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/notification"
	"ambigo-backend/internal/offer"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/places"
	"ambigo-backend/internal/referral"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/storage"
	"ambigo-backend/internal/telephony"
	"ambigo-backend/internal/translation"
	"ambigo-backend/internal/websocket"
	migrations "ambigo-backend/migrations"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

func main() {
	log = logger.Log

	// Load .env file (if it exists)
	if err := godotenv.Load(); err != nil {
		log.Warn().Err(err).Msg("No .env file found, relying on system environment variables")
	}

	log.Info().Msg("Starting Ambigo Backend V2 (Postgres)...")

	// 1. Load Configuration
	appConfig := config.LoadConfig()

	// 2. Initialize PostgreSQL (primary) — replaces MongoDB
	pgCfg := config.PostgresConfig{
		DatabaseURL:       appConfig.DatabaseURL,
		MaxOpenConns:      appConfig.PGMaxOpenConns,
		MinOpenConns:      appConfig.PGMinOpenConns,
		MaxConnLifetime:   appConfig.PGMaxConnLifetime,
		MaxConnIdleTime:   appConfig.PGMaxConnIdleTime,
		HealthCheckPeriod: 30 * time.Second,
	}
	pool, err := config.InitPostgres(context.Background(), pgCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to PostgreSQL")
	}

	// Run goose migrations (embedded via migrations/embed.go)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatal().Err(err).Msg("Failed to set goose dialect")
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	if err := goose.Up(sqlDB, "."); err != nil {
		log.Fatal().Err(err).Msg("Failed to run Postgres migrations")
	}
	_ = sqlDB.Close()
	log.Info().Msg("Postgres migrations applied")

	// 3. Setup EventBus (Pub/Sub for internal messaging)
	eventBus := eventbus.NewInMemoryBus()

	// 4. Setup Interfaces (Live State)
	locationStore := location.NewMemoryStore()
	locationStore.StartCleanupWorker() // Start background sweeper

	// Initialize Stores — single Postgres pool (replaces 4 Mongo DBs)
	authStore := auth.NewStore(pool)
	rideStore := ride.NewStore(pool)
	paymentStore := payment.NewStore(pool)
	adminStore := admin.NewStore(pool)
	counterStore := admin.NewCounterStore(pool)
	hospitalStore := admin.NewHospitalStore(pool)
	hospitalCityStore := admin.NewHospitalCityStore(pool)
	pendingHospitalStore := admin.NewPendingHospitalStore(pool)
	offerStore := offer.NewStore(pool)
	walletStore := payment.NewWalletStore(pool)
	feedbackStore := ride.NewFeedbackStore(pool)
	referralStore := referral.NewStore(pool)

	// Initialize Services & Dispatcher
	referralService := referral.NewService(referralStore, authStore, offerStore, walletStore, eventBus)
	wsManager := websocket.NewManager(locationStore, authStore, eventBus)
	go wsManager.Run() // Start WebSocket Hub

	// Re-arm the session gate from persistent state so a backend restart does
	// not let stale-session connections slip back in for their JWT's lifetime.
	if seed, err := authStore.CurrentSessions(context.Background()); err != nil {
		log.Warn().Err(err).Msg("Failed to seed current sessions")
	} else if len(seed) > 0 {
		wsManager.SeedCurrentSessions(seed)
		log.Info().Int("sessions", len(seed)).Msg("Seeded current sessions from database")
	}

	routeClient := dispatch.NewRouteClient(appConfig.GoogleMapsAPIKey, appConfig.GoogleRoutesAPIURL)
	fcmClient := notification.NewFCMClient(context.Background(), appConfig.FirebaseCredentialsPath)

	// Hospital seeding: Google Places -> H3-bucketed hospitals, refreshed on a
	// 30-day cadence. Falls back gracefully when no Google key is set.
	placesClient := places.NewPlacesClient(appConfig.GoogleMapsAPIKey, appConfig.GooglePlacesAPIURL)
	hospitalSeeder := hospital.NewSeeder(placesClient, hospitalStore, hospitalCityStore, counterStore)
	hospitalSeeder.StartRefresh()

	ambTypes, _ := adminStore.ListAmbulanceTypes(context.Background())
	ambTypeNames := make(map[string]string, len(ambTypes))
	for _, t := range ambTypes {
		ambTypeNames[t.ID] = t.Name
	}
	matcher := dispatch.NewMatcher(locationStore, routeClient, ambTypeNames)
	dispatcher := dispatch.NewDispatcher(matcher, rideStore, eventBus, wsManager)
	dispatcher.StartStaleRideCleanup()

	// Set Google Translate API URL (used by package-level var)
	translation.TranslateAPIURL = appConfig.GoogleTranslateAPIURL

	rzpService := payment.NewRazorpayService(appConfig.RazorpayKeyID, appConfig.RazorpayKeySecret)
	cloudshopeService := telephony.NewCloudshopeService(appConfig.CloudshopeToken, appConfig.CloudshopeNumber, appConfig.CloudshopeAPIBaseURL, appConfig.SMSCC)
	zwitchService := payment.NewZwitchService(appConfig.ZwitchKey, appConfig.ZwitchSecret, appConfig.ZwitchAccountID, appConfig.ZwitchAPIBaseURL, appConfig.ZwitchProxyURL)

	// Initialize Handlers
	rideHandler := handlers.NewRideHandler(dispatcher, eventBus, paymentStore, rzpService, authStore, adminStore, routeClient, walletStore, referralService)
	smsCfg := auth.SMSCountryConfig{
		APIKey:     os.Getenv("SMS_COUNTRY_KEY"),
		APIToken:   os.Getenv("SMS_COUNTRY_TOKEN"),
		APIBaseURL: appConfig.SMSAPIBaseURL,
		SenderID:   appConfig.SMSSenderID,
		CC:         appConfig.SMSCC,
	}
	storageService, err := storage.NewStorageService(context.Background(), appConfig.GCSBucketName)
	if err != nil {
		logger.Log.Warn().Err(err).Msg("GCS Storage Service failed to initialize; falling back to DB storage")
	}

	authHandler := handlers.NewAuthHandler(authStore, eventBus, appConfig.JWTSecret, smsCfg, appConfig.AllowStaleRefreshChain, referralService)
	profileHandler := handlers.NewProfileHandler(authStore)
	verificationHandler := handlers.NewVerificationHandler(authStore, storageService)
	mediaHandler := handlers.NewMediaHandler(storageService, appConfig.JWTSecret)
	paymentHandler := handlers.NewPaymentHandler(paymentStore, eventBus, rzpService, walletStore, appConfig.RazorpayWebhookSecret)
	adminHandler := handlers.NewAdminHandler(adminStore, authStore, eventBus, hospitalStore, hospitalCityStore, pendingHospitalStore, counterStore, rideStore, appConfig.JWTSecret, smsCfg)
	hospitalAuthHandler := handlers.NewHospitalAuthHandler(authStore, pendingHospitalStore, hospitalStore, appConfig.JWTSecret, smsCfg)
	receptionistHandler := handlers.NewHospitalReceptionistHandler(authStore, appConfig.JWTSecret)
	attendantAuthHandler := handlers.NewAttendantAuthHandler(authStore, appConfig.JWTSecret, smsCfg)
	driverAttendantHandler := handlers.NewDriverAttendantHandler(authStore)
	hospitalDashboardHandler := handlers.NewHospitalDashboardHandler(rideStore, hospitalStore, adminStore, authStore, wsManager)
	offerHandler := handlers.NewOfferHandler(offerStore, eventBus)
	sharedHandler := handlers.NewSharedHandler(cloudshopeService, counterStore, adminStore, hospitalStore, hospitalSeeder)
	walletHandler := handlers.NewWalletHandler(authStore, eventBus, walletStore, zwitchService)
	feedbackHandler := handlers.NewFeedbackHandler(feedbackStore)
	referralHandler := handlers.NewReferralHandler(referralStore, referralService)

	// V16: Audit persistence (Postgres, TTL 30d via periodic DELETE)
	auditStore := admin.NewAuditStore(pool)

	// Subscribe EventBus Subscribers
	websocket.NewWSNotifier(wsManager).SubscribeTo(eventBus)
	eventbus.NewFCMNotifier(fcmClient, authStore).SubscribeTo(eventBus)
	eventbus.NewMetricsCollector().SubscribeTo(eventBus)
	eventbus.NewCacheInvalidator(counterStore).SubscribeTo(eventBus)
	eventbus.NewAuditLogger(auditStore).SubscribeTo(eventBus)
	eventbus.NewAnalyticsTracker().SubscribeTo(eventBus)

	// Middlewares
	var jwtOpts []jwt.ParserOption
	if appConfig.JWTAudience != "" {
		jwtOpts = append(jwtOpts, jwt.WithAudience(appConfig.JWTAudience))
	}
	if appConfig.JWTIssuer != "" {
		jwtOpts = append(jwtOpts, jwt.WithIssuer(appConfig.JWTIssuer))
	}
	jwtAuth := middleware.JWTAuth(appConfig.JWTSecret, jwtOpts...)
	requireUser := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("user", next)) }
	requireDriver := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("driver", next)) }
	requireUnvrfDriver := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("unvrf_driver", next)) }
	requireDriverOrUnvrf := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireAnyRole([]string{"driver", "unvrf_driver"}, next)) }
	requireAdmin := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("admin", next)) }
	requireHospitalMD := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("hospital_md", next)) }
	requireAnyHospital := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireAnyRole([]string{"hospital_md", "hospital_receptionist"}, next)) }
	requireUserOrAttendant := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireAnyRole([]string{"user", "attendant"}, next)) }
	requireAttendant := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("attendant", next)) }
	// A6: Admin sub-role enforcement (backward compat — empty role = full access)
	adminAllowedRoles := []string{"super_admin", "admin", ""}
	requireAdminRole := func(allowedRoles []string, next http.HandlerFunc) http.Handler {
		return jwtAuth(middleware.RequireRole("admin", middleware.RequireAdminPermission(allowedRoles, next)))
	}

	// Rate limiters
	otpIPLimiter := middleware.NewIPLimiter(3, 5)                     // V1: IP-based for OTP request
	verifyMobileLimiter := middleware.NewMobileRateLimiter(3, 5)      // V1: mobile-based for OTP verify
	adminLoginLimiter := middleware.NewIPLimiter(5, 10)               // V7: IP-based for admin login
	globalRateLimiter := middleware.NewIPLimiter(100, 200)            // A5: Global per-IP limit for all authenticated routes

	mux := http.NewServeMux()

	apiKeyAuth := middleware.APIKeyAuth(appConfig.APIKey)

	// Metrics endpoint for Prometheus scraping (no API key needed)
	mux.Handle("GET /metrics", promhttp.Handler())

	// pprof endpoint — gated behind ENABLE_PPROF and API-key guard (not public like /metrics).
	// Blank import _ "net/http/pprof" registers handlers on http.DefaultServeMux.
	// Only mounted when ENABLE_PPROF=="true" and always wrapped with apiKeyAuth.
	// Timeout note: main server retains ReadTimeout 10s / WriteTimeout 30s. pprof
	// profiles (heap, goroutine) finish within 30s; long profiles (trace/profile
	// with ?seconds=30) would be truncated by WriteTimeout. To avoid raising the
	// main server's timeout, a loopback-only secondary server with WriteTimeout=0
	// is started for extended traces, while the main mux keeps 30s.
	if os.Getenv("ENABLE_PPROF") == "true" {
		pprofHandler := apiKeyAuth(http.DefaultServeMux)
		mux.Handle("/debug/pprof/", pprofHandler)
		mux.Handle("/debug/pprof/cmdline", pprofHandler)
		mux.Handle("/debug/pprof/profile", pprofHandler)
		mux.Handle("/debug/pprof/symbol", pprofHandler)
		mux.Handle("/debug/pprof/trace", pprofHandler)
		log.Info().Msg("pprof enabled at /debug/pprof/ (API-key guarded; main WriteTimeout 30s retained)")
		go func() {
			pprofMux := http.NewServeMux()
			pprofMux.Handle("/debug/pprof/", http.DefaultServeMux)
			pprofMux.Handle("/debug/pprof/cmdline", http.DefaultServeMux)
			pprofMux.Handle("/debug/pprof/profile", http.DefaultServeMux)
			pprofMux.Handle("/debug/pprof/symbol", http.DefaultServeMux)
			pprofMux.Handle("/debug/pprof/trace", http.DefaultServeMux)
			guarded := apiKeyAuth(pprofMux)
			srv := &http.Server{
				Addr:              "127.0.0.1:6060",
				Handler:           guarded,
				ReadTimeout:       10 * time.Second,
				WriteTimeout:      0, // unlimited for long pprof traces
				ReadHeaderTimeout: 5 * time.Second,
				MaxHeaderBytes:    1 << 20,
			}
			log.Info().Msg("pprof secondary server listening on 127.0.0.1:6060 (WriteTimeout=0)")
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("pprof secondary server stopped")
			}
		}()
	}

	// Basic Health Check (no API key needed)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		postgresOK := "ok"
		if err := pool.Ping(ctx); err != nil {
			postgresOK = "unreachable"
		}
		stat := pool.Stat()
		googleOK := "ok"
		if appConfig.GoogleMapsAPIKey == "" {
			googleOK = "not_configured"
		}

		status := http.StatusOK
		overall := "ok"
		if postgresOK != "ok" {
			overall = "degraded"
			status = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  overall,
			"version": "v2-pg",
			"checks": map[string]interface{}{
				"postgres":           postgresOK,
				"googleap":           googleOK,
				"pool_total_conns":   stat.TotalConns(),
				"pool_acquired_conns": stat.AcquiredConns(),
				"pool_idle_conns":    stat.IdleConns(),
				"pool_max_conns":     stat.MaxConns(),
				"pool_constructing_conns": fmt.Sprintf("%d", stat.ConstructingConns()),
			},
		})
	})

	// WebSocket Endpoint (Needs Auth?)
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		handlers.ServeWS(wsManager, appConfig, w, r)
	})

	// Shared Endpoints
	mux.Handle("POST /api/v2/shared/call/mask", jwtAuth(http.HandlerFunc(sharedHandler.HandleCallMask)))
	mux.Handle("POST /api/v2/shared/updates/ambulance_types/check", http.HandlerFunc(sharedHandler.HandleCheckAmbulanceUpdates))
	mux.Handle("POST /api/v2/shared/ambulance/types/list", http.HandlerFunc(sharedHandler.HandleListAmbulanceTypes)) // Note: V1 POST without Auth for lists? Actually V1 doesn't have auth for /list.
	mux.Handle("POST /api/v2/shared/updates/hospitals/check", http.HandlerFunc(sharedHandler.HandleCheckHospitalUpdates))
	mux.Handle("POST /api/v2/shared/hospitals/list", http.HandlerFunc(sharedHandler.HandleListHospitals))
	mux.Handle("POST /api/v2/shared/feedback/submit", jwtAuth(http.HandlerFunc(feedbackHandler.HandleSubmitFeedback)))

	// Payment Endpoints (Protected)
	mux.Handle("POST /api/v2/payment/pending", jwtAuth(http.HandlerFunc(paymentHandler.HandleGetPending)))
	mux.Handle("POST /api/v2/payment/ride", jwtAuth(http.HandlerFunc(paymentHandler.HandleGetByRide)))
	mux.Handle("POST /api/v2/payment/user/process", requireUser(http.HandlerFunc(paymentHandler.HandleProcessUserPayment)))
	mux.Handle("POST /api/v2/payment/user/process-cash", requireUser(http.HandlerFunc(paymentHandler.HandleProcessUserCashPayment)))
	mux.Handle("POST /api/v2/payment/driver/process", requireDriver(http.HandlerFunc(paymentHandler.HandleProcessDriverPayment)))
	mux.HandleFunc("POST /api/v2/payment/webhook/razorpay", paymentHandler.HandleRazorpayWebhook)

	// Wallet Endpoints (Protected)
	mux.Handle("POST /api/v2/driver/wallet/get", requireDriver(http.HandlerFunc(walletHandler.HandleGetWallet)))
	mux.Handle("POST /api/v2/driver/wallet/update", requireDriver(http.HandlerFunc(walletHandler.HandleUpdateWallet)))
	mux.Handle("POST /api/v2/driver/wallet/withdraw", requireDriver(http.HandlerFunc(walletHandler.HandleWithdraw)))
	mux.Handle("POST /api/v2/driver/wallet/transactions/list", requireDriver(http.HandlerFunc(walletHandler.HandleListTransactions)))
	// Driver attendant (one per ambulance, driver creates)
	mux.Handle("POST /api/v2/driver/attendant/create", requireDriver(http.HandlerFunc(driverAttendantHandler.HandleDriverCreateAttendant)))
	mux.Handle("POST /api/v2/driver/attendant/list", requireDriver(http.HandlerFunc(driverAttendantHandler.HandleDriverListAttendants)))
	mux.Handle("POST /api/v2/driver/attendant/delete", requireDriver(http.HandlerFunc(driverAttendantHandler.HandleDriverDeleteAttendant)))

	// Ride Endpoints (Protected)
	mux.Handle("POST /api/v2/rides/request", requireUser(http.HandlerFunc(rideHandler.HandleRequestRide)))
	mux.Handle("POST /api/v2/rides/{id}/accept", requireDriver(http.HandlerFunc(rideHandler.HandleDriverAccept)))
	mux.Handle("POST /api/v2/rides/{id}/arrive", requireDriver(http.HandlerFunc(rideHandler.HandleArrive)))
	mux.Handle("POST /api/v2/rides/{id}/start", requireDriver(http.HandlerFunc(rideHandler.HandleStart)))
	mux.Handle("POST /api/v2/rides/{id}/complete", requireDriver(http.HandlerFunc(rideHandler.HandleComplete)))
	// Both Users and Drivers can cancel
	mux.Handle("POST /api/v2/rides/{id}/cancel", jwtAuth(http.HandlerFunc(rideHandler.HandleCancel)))
	mux.Handle("POST /api/v2/rides/history", jwtAuth(http.HandlerFunc(rideHandler.HandleGetHistory)))
	mux.Handle("POST /api/v2/rides/current", jwtAuth(http.HandlerFunc(rideHandler.HandleGetCurrentRide)))
	mux.Handle("POST /api/v2/rides/driver/details", jwtAuth(http.HandlerFunc(rideHandler.HandleGetDriverDetails)))
	mux.Handle("POST /api/v2/rides/user/details", jwtAuth(http.HandlerFunc(rideHandler.HandleGetUserDetails)))
	mux.Handle("POST /api/v2/rides/feedback/list", requireDriver(http.HandlerFunc(feedbackHandler.HandleListFeedback)))
	mux.Handle("POST /api/v2/route", jwtAuth(http.HandlerFunc(rideHandler.HandleRoutePreview)))
	mux.Handle("POST /api/v2/fare/estimate", jwtAuth(http.HandlerFunc(rideHandler.HandleFareEstimate)))

	// Auth Endpoints (Public) — V1: OTP request rate limited by IP, OTP verify rate limited by mobile
	mux.HandleFunc("POST /api/v2/auth/user/request-otp", middleware.RateLimit(authHandler.HandleUserRequestOTP, otpIPLimiter))
	mux.HandleFunc("POST /api/v2/auth/user/verify-otp", middleware.RateLimitMobile(authHandler.HandleUserVerifyOTP, verifyMobileLimiter))
	mux.HandleFunc("POST /api/v2/auth/driver/request-otp", middleware.RateLimit(authHandler.HandleDriverRequestOTP, otpIPLimiter))
	mux.HandleFunc("POST /api/v2/auth/driver/verify-otp", middleware.RateLimitMobile(authHandler.HandleDriverVerifyOTP, verifyMobileLimiter))
	// V8: Token refresh, V4: Logout, V18: Sessions
	mux.HandleFunc("POST /api/v2/auth/refresh", authHandler.HandleRefreshToken)
	mux.Handle("POST /api/v2/auth/logout", jwtAuth(http.HandlerFunc(authHandler.HandleLogout)))
	mux.Handle("POST /api/v2/auth/sessions", jwtAuth(http.HandlerFunc(authHandler.HandleListSessions)))
	mux.Handle("POST /api/v2/auth/sessions/revoke", jwtAuth(http.HandlerFunc(authHandler.HandleRevokeSession)))

	// Profile Endpoints (Protected)
	mux.Handle("POST /api/v2/user/profile", requireUser(profileHandler.HandleGetUserProfile))
	mux.Handle("POST /api/v2/user/fcm", requireUser(profileHandler.HandleUpdateUserFCM))

	// Referral Endpoints (Protected)
	mux.Handle("POST /api/v2/referral/rewards", jwtAuth(http.HandlerFunc(referralHandler.HandleGetRewards)))

	// V10: driver/profile and driver/fcm allow both driver and unvrf_driver roles
	mux.Handle("POST /api/v2/driver/profile", requireDriverOrUnvrf(profileHandler.HandleGetDriverProfile))
	mux.Handle("POST /api/v2/driver/fcm", requireDriverOrUnvrf(profileHandler.HandleUpdateDriverFCM))

	// Verification & Media Endpoints (Protected)
	mux.Handle("POST /api/v2/driver/verification/check", jwtAuth(http.HandlerFunc(verificationHandler.HandleCheckVerification)))
	mux.Handle("POST /api/v2/driver/verification/update", requireUnvrfDriver(verificationHandler.HandleUpdateVerification))
	mux.HandleFunc("/api/v2/admin/migrate-images-to-gcs", verificationHandler.HandleMigrateImagesToGCS)
	mux.HandleFunc("GET /api/v2/media/view", mediaHandler.HandleViewMedia)

	// Admin Endpoints (Protected)
	// V7: Rate limit admin login (username/password — IP-based)
	mux.HandleFunc("POST /api/v2/admin/login", middleware.RateLimit(adminHandler.HandleAdminLogin, adminLoginLimiter))
	// Admin OTP login: request IP-limited, verify mobile-limited
	mux.HandleFunc("POST /api/v2/admin/login/mobile", middleware.RateLimit(adminHandler.HandleAdminMobileRequestOTP, otpIPLimiter))
	mux.HandleFunc("POST /api/v2/admin/login/mobile/verify", middleware.RateLimitMobile(adminHandler.HandleAdminMobileVerifyOTP, verifyMobileLimiter))
	// V19: Admin password change
	mux.Handle("POST /api/v2/admin/password", requireAdmin(http.HandlerFunc(adminHandler.HandleAdminPasswordChange)))
	mux.Handle("POST /api/v2/admin/ambulance_types", requireAdmin(http.HandlerFunc(adminHandler.HandleCreateAmbulanceType)))
	mux.Handle("GET /api/v2/admin/ambulance_types", requireAdmin(http.HandlerFunc(adminHandler.HandleListAmbulanceTypes)))
	mux.Handle("DELETE /api/v2/admin/ambulance_types/{id}", requireAdmin(http.HandlerFunc(adminHandler.HandleDeleteAmbulanceType)))
	// Admin: Verified Driver CRUD
	mux.Handle("POST /api/v2/admin/drivers/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListDrivers)))
	mux.Handle("POST /api/v2/admin/drivers/details", requireAdmin(http.HandlerFunc(adminHandler.HandleGetDriverDetails)))
	mux.Handle("POST /api/v2/admin/drivers/add", requireAdmin(http.HandlerFunc(adminHandler.HandleAddDriver)))
	mux.Handle("POST /api/v2/admin/drivers/update", requireAdmin(http.HandlerFunc(adminHandler.HandleUpdateDriver)))
	mux.Handle("POST /api/v2/admin/drivers/delete", requireAdmin(http.HandlerFunc(adminHandler.HandleDeleteDriver)))
	// Admin: Unverified Driver Flow
	mux.Handle("POST /api/v2/admin/drivers/unverified/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListUnverifiedDrivers)))
	mux.Handle("POST /api/v2/admin/drivers/unverified/list/all", requireAdmin(http.HandlerFunc(adminHandler.HandleListAllUnverifiedDrivers)))
	mux.Handle("POST /api/v2/admin/drivers/unverified/fetch", requireAdmin(http.HandlerFunc(adminHandler.HandleFetchUnverifiedDriver)))
	mux.Handle("POST /api/v2/admin/drivers/unverified/accept", requireAdminRole(adminAllowedRoles, adminHandler.HandleAcceptDriver))
	mux.Handle("POST /api/v2/admin/drivers/unverified/reject", requireAdminRole(adminAllowedRoles, adminHandler.HandleRejectDriver))
	mux.Handle("POST /api/v2/admin/drivers/unverified/counter", requireAdmin(http.HandlerFunc(adminHandler.HandleUnverifiedDriverCounter)))
	// Admin: Driver Ride History
	mux.Handle("POST /api/v2/admin/drivers/rides/list", requireAdmin(http.HandlerFunc(adminHandler.HandleDriverRideList)))
	// Admin: Profile
	mux.Handle("POST /api/v2/admin/profile/fcm", requireAdmin(http.HandlerFunc(adminHandler.HandleAdminFCMUpdate)))
	mux.Handle("POST /api/v2/admin/profile/location", requireAdmin(http.HandlerFunc(adminHandler.HandleAdminLocationUpdate)))
	// Admin: Users
	mux.Handle("POST /api/v2/admin/users/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListUsers)))
	// Admin: User Rides
	mux.Handle("POST /api/v2/admin/rides/user/list", requireAdmin(http.HandlerFunc(adminHandler.HandleUserRideList)))
	// Admin: Ride Listing
	mux.Handle("POST /api/v2/admin/rides/completed/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListCompletedRides)))
	mux.Handle("POST /api/v2/admin/rides/ongoing/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListOngoingRides)))
	// Admin: Ambulance Type Update
	mux.Handle("POST /api/v2/admin/ambulance/types/update", requireAdmin(http.HandlerFunc(adminHandler.HandleUpdateAmbulanceType)))
	mux.Handle("POST /api/v2/admin/feedback/list", requireAdmin(http.HandlerFunc(feedbackHandler.HandleAdminListFeedback)))
	mux.Handle("POST /api/v2/admin/hospitals/add", requireAdmin(http.HandlerFunc(adminHandler.HandleAddHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/update", requireAdmin(http.HandlerFunc(adminHandler.HandleUpdateHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/delete", requireAdmin(http.HandlerFunc(adminHandler.HandleDeleteHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/sync", requireAdmin(http.HandlerFunc(sharedHandler.HandleSyncHospitals)))
	// Admin: Hospital service areas (cities)
	mux.Handle("POST /api/v2/admin/hospital/cities/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListHospitalCities)))
	mux.Handle("POST /api/v2/admin/hospital/cities/add", requireAdmin(http.HandlerFunc(adminHandler.HandleAddHospitalCity)))
	mux.Handle("POST /api/v2/admin/hospital/cities/update", requireAdmin(http.HandlerFunc(adminHandler.HandleUpdateHospitalCity)))
	mux.Handle("POST /api/v2/admin/hospital/cities/delete", requireAdmin(http.HandlerFunc(adminHandler.HandleDeleteHospitalCity)))
	mux.Handle("POST /api/v2/admin/hospital/cities/sync", requireAdmin(http.HandlerFunc(sharedHandler.HandleSyncHospitals)))
	// Hospital MD (public signup + OTP)
	mux.HandleFunc("POST /api/v2/hospital/md/request-otp", middleware.RateLimit(hospitalAuthHandler.HandleHospitalMDRequestOTP, otpIPLimiter))
	mux.HandleFunc("POST /api/v2/hospital/md/login/request-otp", middleware.RateLimit(hospitalAuthHandler.HandleHospitalMDLoginRequestOTP, otpIPLimiter))
	mux.HandleFunc("POST /api/v2/hospital/md/signup", middleware.RateLimitMobile(hospitalAuthHandler.HandleHospitalMDSignup, verifyMobileLimiter))
	mux.HandleFunc("POST /api/v2/hospital/md/login/mobile/verify", middleware.RateLimitMobile(hospitalAuthHandler.HandleHospitalMDVerifyOTP, verifyMobileLimiter))
	mux.HandleFunc("POST /api/v2/hospital/md/login/password", middleware.RateLimit(hospitalAuthHandler.HandleHospitalMDLoginPassword, adminLoginLimiter))
	mux.Handle("POST /api/v2/hospital/md/setup-password", requireHospitalMD(http.HandlerFunc(hospitalAuthHandler.HandleHospitalMDSetupPassword)))
	mux.Handle("POST /api/v2/hospital/auth/me", requireHospitalMD(http.HandlerFunc(hospitalAuthHandler.HandleHospitalMDMe)))
	// Attendant (public, inside driver app)
	mux.HandleFunc("POST /api/v2/attendant/request-otp", middleware.RateLimit(attendantAuthHandler.HandleAttendantRequestOTP, otpIPLimiter))
	mux.HandleFunc("POST /api/v2/attendant/verify-otp", middleware.RateLimitMobile(attendantAuthHandler.HandleAttendantVerifyOTP, verifyMobileLimiter))
	// Admin pending hospitals queue
	mux.Handle("POST /api/v2/admin/hospitals/pending/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListPendingHospitals)))
	mux.Handle("POST /api/v2/admin/hospitals/pending/fetch", requireAdmin(http.HandlerFunc(adminHandler.HandleFetchPendingHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/pending/approve", requireAdmin(http.HandlerFunc(adminHandler.HandleApprovePendingHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/pending/reject", requireAdmin(http.HandlerFunc(adminHandler.HandleRejectPendingHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/pending/counter", requireAdmin(http.HandlerFunc(adminHandler.HandlePendingHospitalCounter)))
	mux.Handle("POST /api/v2/admin/hospital/mds/list", requireAdmin(http.HandlerFunc(adminHandler.HandleListHospitalMDs)))
	mux.Handle("POST /api/v2/admin/hospital/mds/ban", requireAdmin(http.HandlerFunc(adminHandler.HandleBanHospitalMD)))
	mux.Handle("POST /api/v2/admin/hospital/mds/unban", requireAdmin(http.HandlerFunc(adminHandler.HandleUnbanHospitalMD)))
	mux.Handle("POST /api/v2/admin/hospital/mds/delete", requireAdmin(http.HandlerFunc(adminHandler.HandleDeleteHospitalMD)))
	// Hospital receptionist (MD creates in admin app, receptionist logs into website)
	mux.Handle("POST /api/v2/hospital/receptionist/create", requireHospitalMD(http.HandlerFunc(receptionistHandler.HandleCreateReceptionist)))
	mux.Handle("POST /api/v2/hospital/receptionists/list", requireHospitalMD(http.HandlerFunc(receptionistHandler.HandleListReceptionists)))
	mux.Handle("POST /api/v2/hospital/receptionist/delete", requireHospitalMD(http.HandlerFunc(receptionistHandler.HandleDeleteReceptionist)))
	mux.HandleFunc("POST /api/v2/hospital/receptionist/login", middleware.RateLimit(receptionistHandler.HandleReceptionistLogin, adminLoginLimiter))
	mux.Handle("POST /api/v2/hospital/receptionist/me", requireAnyHospital(http.HandlerFunc(receptionistHandler.HandleReceptionistMe)))
	// Hospital dashboard (for receptionist/MD)
	mux.Handle("POST /api/v2/hospital/dashboard/stats", requireAnyHospital(http.HandlerFunc(hospitalDashboardHandler.HandleHospitalStats)))
	mux.Handle("POST /api/v2/hospital/rides/incoming/list", requireAnyHospital(http.HandlerFunc(hospitalDashboardHandler.HandleHospitalIncomingRides)))
	mux.Handle("POST /api/v2/hospital/rides/history", requireAnyHospital(http.HandlerFunc(hospitalDashboardHandler.HandleHospitalHistory)))
	mux.Handle("POST /api/v2/hospital/rides/detail", requireAnyHospital(http.HandlerFunc(hospitalDashboardHandler.HandleHospitalRideDetail)))
	mux.Handle("POST /api/v2/hospital/profile", requireAnyHospital(http.HandlerFunc(hospitalDashboardHandler.HandleHospitalProfile)))
	mux.Handle("POST /api/v2/hospital/profile/update", requireHospitalMD(http.HandlerFunc(hospitalDashboardHandler.HandleUpdateHospitalProfile)))
	mux.Handle("POST /api/v2/hospital/analytics", requireAnyHospital(http.HandlerFunc(hospitalDashboardHandler.HandleHospitalAnalytics)))
	// In-ride patient condition update (ALS/BLS/Emergency/SOS) — user or attendant
	mux.Handle("POST /api/v2/rides/condition", requireUserOrAttendant(http.HandlerFunc(hospitalDashboardHandler.HandleUpdateRideCondition)))
	mux.Handle("POST /api/v2/rides/{id}/condition", requireUserOrAttendant(http.HandlerFunc(hospitalDashboardHandler.HandleUpdateRideCondition)))
	// Attendant: get hospital contact for a ride
	mux.Handle("POST /api/v2/attendant/hospital/contact", requireAttendant(http.HandlerFunc(hospitalDashboardHandler.HandleAttendantHospitalContact)))
	mux.Handle("POST /api/v2/attendant/rides/current", requireAttendant(http.HandlerFunc(hospitalDashboardHandler.HandleAttendantCurrentRide)))
	mux.Handle("POST /api/v2/admin/offers", requireAdmin(http.HandlerFunc(offerHandler.HandleCreate)))
	mux.Handle("GET /api/v2/admin/offers", requireAdmin(http.HandlerFunc(offerHandler.HandleList)))
	mux.Handle("DELETE /api/v2/admin/offers/{id}", requireAdmin(http.HandlerFunc(offerHandler.HandleDelete)))
	// Admin: Referral Config
	mux.Handle("GET /api/v2/admin/referral/config", requireAdmin(http.HandlerFunc(referralHandler.HandleGetConfig)))
	mux.Handle("POST /api/v2/admin/referral/config", requireAdmin(http.HandlerFunc(referralHandler.HandleSaveConfig)))

	// Housekeeping: audit_log TTL 30d + auth_otp 5m cleanup (replaces Mongo TTL indexes)
	// OTP rows expire after 5m — ticker must match (was 1h => 12x bloat of expired rows)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := authStore.CleanupExpiredOTPs(ctx); err != nil {
				log.Error().Err(err).Msg("auth_otp cleanup failed")
			}
			if _, err := auditStore.CleanupOldLogs(ctx); err != nil {
				log.Error().Err(err).Msg("audit_log cleanup failed")
			}
			if _, err := authStore.CleanupExpiredRefreshTokens(ctx); err != nil {
				log.Error().Err(err).Msg("refresh_tokens cleanup failed")
			}
			cancel()
		}
	}()

	// Apply API key auth + global rate limiter to all routes except /metrics, /health, and /ws
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/metrics" || path == "/api/v1/health" || path == "/ws" || path == "/api/v2/payment/webhook/razorpay" {
			mux.ServeHTTP(w, r)
			return
		}
		middleware.RateLimitMiddleware(globalRateLimiter, apiKeyAuth(mux)).ServeHTTP(w, r)
	})
	// sql is used via stdlib.OpenDBFromPool for goose
	server := &http.Server{
		Addr:              ":" + appConfig.Port,
		Handler:           middleware.CORS(middleware.RequestID(middleware.Metrics(middleware.BodyLimit(protected)))),
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info().Str("port", appConfig.Port).Msg("Server listening (Postgres)")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("Server stopped")
		}
	}()

	sig := <-quit
	log.Warn().Str("signal", sig.String()).Msg("Starting graceful shutdown (20s drain)")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("HTTP server forced shutdown")
	}

	// pgxpool.Close() is deferred via pool.Close() above (no context needed)
	pool.Close()

	log.Info().Msg("Server exited cleanly")
}
