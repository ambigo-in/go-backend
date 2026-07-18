package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"encoding/json"

	"ambigo-backend/api/handlers"
	"ambigo-backend/api/middleware"
	"ambigo-backend/config"
	"ambigo-backend/internal/admin"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/dispatch"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/notification"
	"ambigo-backend/internal/offer"
	"ambigo-backend/internal/payment"
	"ambigo-backend/internal/referral"
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/telephony"
	"ambigo-backend/internal/translation"
	"ambigo-backend/internal/websocket"

	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var log zerolog.Logger

func main() {
	log = logger.Log

	// Load .env file (if it exists)
	if err := godotenv.Load(); err != nil {
		log.Warn().Err(err).Msg("No .env file found, relying on system environment variables")
	}

	log.Info().Msg("Starting Ambigo Backend V2...")

	// 1. Load Configuration
	appConfig := config.LoadConfig()

	// 2. Initialize MongoDB (Business Truth)
	client, err := config.InitMongoDB(appConfig.MongoURI)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to MongoDB")
	}
	if err := config.EnsureIndexes(client); err != nil {
		log.Fatal().Err(err).Msg("Failed to ensure MongoDB indexes")
	}

	// 3. Setup EventBus (Pub/Sub for internal messaging)
	eventBus := eventbus.NewInMemoryBus()

	// 4. Setup Interfaces (Live State)
	locationStore := location.NewMemoryStore()
	locationStore.StartCleanupWorker() // Start background sweeper

	// Initialize Stores — mapped to V1 Python multi-database layout:
	// V1: Users DB → users, drivers, unverified_drivers, auth_otp, admins
	// V1: Rides DB → searching_rides, accepted_rides, ongoing_rides, completed_rides (V2 uses single "rides" collection with status field)
	// V1: Records DB → payments, wallet_transactions, feedback, referrals, offers
	// V1: Data DB → ambulance_types, hospitals, counters
	usersDB := client.Database("Users")
	ridesDB := client.Database("Rides")
	recordsDB := client.Database("Records")
	dataDB := client.Database("Data")

	authStore := auth.NewStore(usersDB, recordsDB)
	rideStore := ride.NewStore(ridesDB.Collection("rides"))
	paymentStore := payment.NewStore(recordsDB)
	adminStore := admin.NewStore(dataDB, usersDB)
	counterStore := admin.NewCounterStore(dataDB)
	hospitalStore := admin.NewHospitalStore(dataDB)
	offerStore := offer.NewStore(recordsDB)
	walletStore := payment.NewWalletStore(recordsDB, usersDB)
	feedbackStore := ride.NewFeedbackStore(recordsDB)
	referralStore := referral.NewStore(dataDB, recordsDB)

	// Initialize Services & Dispatcher
	referralService := referral.NewService(referralStore, authStore, offerStore, walletStore, eventBus)
	wsManager := websocket.NewManager(locationStore, authStore, eventBus)
	go wsManager.Run() // Start WebSocket Hub
	
	routeClient := dispatch.NewRouteClient(appConfig.GoogleMapsAPIKey, appConfig.GoogleRoutesAPIURL)
	fcmClient := notification.NewFCMClient(context.Background(), appConfig.FirebaseCredentialsPath)

	ambTypes, _ := adminStore.ListAmbulanceTypes(context.Background())
	ambTypeNames := make(map[string]string, len(ambTypes))
	for _, t := range ambTypes {
		ambTypeNames[t.ID.Hex()] = t.Name
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
	authHandler := handlers.NewAuthHandler(authStore, eventBus, appConfig.JWTSecret, smsCfg, appConfig.AllowStaleRefreshChain, referralService)
	profileHandler := handlers.NewProfileHandler(authStore)
	verificationHandler := handlers.NewVerificationHandler(authStore)
	paymentHandler := handlers.NewPaymentHandler(paymentStore, eventBus, rzpService, appConfig.RazorpayWebhookSecret)
	adminHandler := handlers.NewAdminHandler(adminStore, authStore, eventBus, hospitalStore, counterStore, rideStore, appConfig.JWTSecret, smsCfg)
	offerHandler := handlers.NewOfferHandler(offerStore, eventBus)
	sharedHandler := handlers.NewSharedHandler(cloudshopeService, counterStore, adminStore, hospitalStore)
	walletHandler := handlers.NewWalletHandler(authStore, eventBus, walletStore, zwitchService)
	feedbackHandler := handlers.NewFeedbackHandler(feedbackStore)
	referralHandler := handlers.NewReferralHandler(referralStore, referralService)

	// V16: Audit persistence
	auditStore := admin.NewAuditStore(recordsDB)

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

	// Basic Health Check (no API key needed)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		mongoOK := "ok"
		if err := client.Ping(ctx, readpref.Primary()); err != nil {
			mongoOK = "unreachable"
		}

		googleOK := "ok"
		if appConfig.GoogleMapsAPIKey == "" {
			googleOK = "not_configured"
		}

		status := http.StatusOK
		overall := "ok"
		if mongoOK != "ok" {
			overall = "degraded"
			status = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  overall,
			"version": "v2",
			"checks": map[string]string{
				"mongodb":  mongoOK,
				"googleap": googleOK,
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
	mux.Handle("POST /api/v2/payment/driver/process", requireDriver(http.HandlerFunc(paymentHandler.HandleProcessDriverPayment)))
	mux.HandleFunc("POST /api/v2/payment/webhook/razorpay", paymentHandler.HandleRazorpayWebhook)

	// Wallet Endpoints (Protected)
	mux.Handle("POST /api/v2/driver/wallet/get", requireDriver(http.HandlerFunc(walletHandler.HandleGetWallet)))
	mux.Handle("POST /api/v2/driver/wallet/update", requireDriver(http.HandlerFunc(walletHandler.HandleUpdateWallet)))
	mux.Handle("POST /api/v2/driver/wallet/withdraw", requireDriver(http.HandlerFunc(walletHandler.HandleWithdraw)))
	mux.Handle("POST /api/v2/driver/wallet/transactions/list", requireDriver(http.HandlerFunc(walletHandler.HandleListTransactions)))

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

	// Verification Endpoints (Protected)
	mux.Handle("POST /api/v2/driver/verification/check", jwtAuth(http.HandlerFunc(verificationHandler.HandleCheckVerification)))
	mux.Handle("POST /api/v2/driver/verification/update", requireUnvrfDriver(verificationHandler.HandleUpdateVerification))

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
	mux.Handle("POST /api/v2/admin/offers", requireAdmin(http.HandlerFunc(offerHandler.HandleCreate)))
	mux.Handle("GET /api/v2/admin/offers", requireAdmin(http.HandlerFunc(offerHandler.HandleList)))
	mux.Handle("DELETE /api/v2/admin/offers/{id}", requireAdmin(http.HandlerFunc(offerHandler.HandleDelete)))
	// Admin: Referral Config
	mux.Handle("GET /api/v2/admin/referral/config", requireAdmin(http.HandlerFunc(referralHandler.HandleGetConfig)))
	mux.Handle("POST /api/v2/admin/referral/config", requireAdmin(http.HandlerFunc(referralHandler.HandleSaveConfig)))

	// Apply API key auth + global rate limiter to all routes except /metrics, /health, and /ws
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/metrics" || path == "/api/v1/health" || path == "/ws" || path == "/api/v2/payment/webhook/razorpay" {
			mux.ServeHTTP(w, r)
			return
		}
		middleware.RateLimitMiddleware(globalRateLimiter, apiKeyAuth(mux)).ServeHTTP(w, r)
	})
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
		log.Info().Str("port", appConfig.Port).Msg("Server listening")
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

		if err := client.Disconnect(ctx); err != nil {
		log.Error().Err(err).Msg("MongoDB disconnect error")
	}

	log.Info().Msg("Server exited cleanly")
}
