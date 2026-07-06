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
	"ambigo-backend/internal/ride"
	"ambigo-backend/internal/telephony"
	"ambigo-backend/internal/translation"
	"ambigo-backend/internal/websocket"

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
	defer client.Disconnect(nil)

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

	// Initialize Services & Dispatcher
	wsManager := websocket.NewManager(locationStore, authStore, eventBus)
	go wsManager.Run() // Start WebSocket Hub
	
	routeClient := dispatch.NewRouteClient(appConfig.GoogleMapsAPIKey, appConfig.GoogleRoutesAPIURL)
	fcmClient := notification.NewFCMClient(context.Background(), appConfig.FirebaseCredentialsPath)
	matcher := dispatch.NewMatcher(locationStore, routeClient)
	dispatcher := dispatch.NewDispatcher(matcher, rideStore, eventBus, wsManager)
	dispatcher.StartStaleRideCleanup()

	// Set Google Translate API URL (used by package-level var)
	translation.TranslateAPIURL = appConfig.GoogleTranslateAPIURL

	rzpService := payment.NewRazorpayService(appConfig.RazorpayKeyID, appConfig.RazorpayKeySecret)
	cloudshopeService := telephony.NewCloudshopeService(appConfig.CloudshopeToken, appConfig.CloudshopeNumber, appConfig.CloudshopeAPIBaseURL, appConfig.SMSCC)
	zwitchService := payment.NewZwitchService(appConfig.ZwitchKey, appConfig.ZwitchSecret, appConfig.ZwitchAccountID, appConfig.ZwitchAPIBaseURL)

	// Initialize Handlers
	rideHandler := handlers.NewRideHandler(dispatcher, eventBus, paymentStore, rzpService, authStore, adminStore, routeClient, walletStore)
	authHandler := handlers.NewAuthHandler(authStore, eventBus, appConfig.JWTSecret, auth.SMSCountryConfig{
		APIKey:     os.Getenv("SMS_COUNTRY_KEY"),
		APIToken:   os.Getenv("SMS_COUNTRY_TOKEN"),
		APIBaseURL: appConfig.SMSAPIBaseURL,
		SenderID:   appConfig.SMSSenderID,
		CC:         appConfig.SMSCC,
	})
	profileHandler := handlers.NewProfileHandler(authStore)
	verificationHandler := handlers.NewVerificationHandler(authStore)
	paymentHandler := handlers.NewPaymentHandler(paymentStore, eventBus, rzpService, appConfig.RazorpayWebhookSecret)
	adminHandler := handlers.NewAdminHandler(adminStore, authStore, eventBus, hospitalStore, counterStore, rideStore, appConfig.JWTSecret)
	offerHandler := handlers.NewOfferHandler(offerStore, eventBus)
	sharedHandler := handlers.NewSharedHandler(cloudshopeService, counterStore, adminStore, hospitalStore)
	walletHandler := handlers.NewWalletHandler(authStore, eventBus, walletStore, zwitchService)
	feedbackHandler := handlers.NewFeedbackHandler(feedbackStore)

	// Subscribe EventBus Subscribers
	websocket.NewWSNotifier(wsManager).SubscribeTo(eventBus)
	eventbus.NewFCMNotifier(fcmClient, authStore).SubscribeTo(eventBus)
	eventbus.NewMetricsCollector().SubscribeTo(eventBus)
	eventbus.NewCacheInvalidator(counterStore).SubscribeTo(eventBus)
	eventbus.NewAuditLogger().SubscribeTo(eventBus)
	eventbus.NewAnalyticsTracker().SubscribeTo(eventBus)

	// Middlewares
	jwtAuth := middleware.JWTAuth(appConfig.JWTSecret)
	requireUser := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("user", next)) }
	requireDriver := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("driver", next)) }
	requireUnvrfDriver := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("unvrf_driver", next)) }
	requireAdmin := func(next http.HandlerFunc) http.Handler { return jwtAuth(middleware.RequireRole("admin", next)) }

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
	mux.Handle("POST /api/v2/route", jwtAuth(http.HandlerFunc(rideHandler.HandleRoutePreview)))
	mux.Handle("POST /api/v2/fare/estimate", jwtAuth(http.HandlerFunc(rideHandler.HandleFareEstimate)))

	// Auth Endpoints (Public) — OTP request has rate limiting
	otpLimiter := middleware.NewIPLimiter(3, 5)
	mux.HandleFunc("POST /api/v2/auth/user/request-otp", middleware.RateLimit(authHandler.HandleUserRequestOTP, otpLimiter))
	mux.HandleFunc("POST /api/v2/auth/user/verify-otp", authHandler.HandleUserVerifyOTP)
	mux.HandleFunc("POST /api/v2/auth/driver/request-otp", middleware.RateLimit(authHandler.HandleDriverRequestOTP, otpLimiter))
	mux.HandleFunc("POST /api/v2/auth/driver/verify-otp", authHandler.HandleDriverVerifyOTP)

	// Profile Endpoints (Protected)
	mux.Handle("POST /api/v2/user/profile", requireUser(profileHandler.HandleGetUserProfile))
	mux.Handle("POST /api/v2/user/fcm", requireUser(profileHandler.HandleUpdateUserFCM))
	
	mux.Handle("POST /api/v2/driver/profile", jwtAuth(http.HandlerFunc(profileHandler.HandleGetDriverProfile)))
	mux.Handle("POST /api/v2/driver/fcm", jwtAuth(http.HandlerFunc(profileHandler.HandleUpdateDriverFCM)))

	// Verification Endpoints (Protected)
	mux.Handle("POST /api/v2/driver/verification/check", jwtAuth(http.HandlerFunc(verificationHandler.HandleCheckVerification)))
	mux.Handle("POST /api/v2/driver/verification/update", requireUnvrfDriver(verificationHandler.HandleUpdateVerification))

	// Admin Endpoints (Protected)
	mux.HandleFunc("POST /api/v2/admin/login", adminHandler.HandleAdminLogin)
	mux.HandleFunc("POST /api/v2/admin/login/mobile", adminHandler.HandleAdminMobileRequestOTP)
	mux.HandleFunc("POST /api/v2/admin/login/mobile/verify", adminHandler.HandleAdminMobileVerifyOTP)
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
	mux.Handle("POST /api/v2/admin/drivers/unverified/accept", requireAdmin(http.HandlerFunc(adminHandler.HandleAcceptDriver)))
	mux.Handle("POST /api/v2/admin/drivers/unverified/reject", requireAdmin(http.HandlerFunc(adminHandler.HandleRejectDriver)))
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
	mux.Handle("POST /api/v2/admin/hospitals/add", requireAdmin(http.HandlerFunc(adminHandler.HandleAddHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/update", requireAdmin(http.HandlerFunc(adminHandler.HandleUpdateHospital)))
	mux.Handle("POST /api/v2/admin/hospitals/delete", requireAdmin(http.HandlerFunc(adminHandler.HandleDeleteHospital)))
	mux.Handle("POST /api/v2/admin/offers", requireAdmin(http.HandlerFunc(offerHandler.HandleCreate)))
	mux.Handle("GET /api/v2/admin/offers", requireAdmin(http.HandlerFunc(offerHandler.HandleList)))
	mux.Handle("DELETE /api/v2/admin/offers/{id}", requireAdmin(http.HandlerFunc(offerHandler.HandleDelete)))

	// Apply API key auth to all routes except /metrics, /health, and /ws (WebSocket validates api_key via query param)
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/metrics" || path == "/api/v1/health" || path == "/ws" {
			mux.ServeHTTP(w, r)
			return
		}
		apiKeyAuth(mux).ServeHTTP(w, r)
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
