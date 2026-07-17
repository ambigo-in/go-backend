package referral

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/offer"
	"ambigo-backend/internal/payment"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // No I, O, 0, 1 to avoid confusion
const codeLength = 6

// Service contains the business logic for the referral system.
type Service struct {
	store       *Store
	authStore   *auth.Store
	offerStore  *offer.Store
	walletStore *payment.WalletStore
	eventBus    *eventbus.InMemoryBus
}

// NewService creates a new referral Service.
func NewService(store *Store, authStore *auth.Store, offerStore *offer.Store, walletStore *payment.WalletStore, eventBus *eventbus.InMemoryBus) *Service {
	return &Service{
		store:       store,
		authStore:   authStore,
		offerStore:  offerStore,
		walletStore: walletStore,
		eventBus:    eventBus,
	}
}

// GenerateReferralCode generates a unique 6-character referral code.
// Uses crypto/rand for unpredictability and checks for collisions.
func (s *Service) GenerateReferralCode(ctx context.Context) (string, error) {
	maxAttempts := 10
	for attempt := 0; attempt < maxAttempts; attempt++ {
		code, err := randomCode(codeLength)
		if err != nil {
			return "", err
		}

		// Check collision against both users and drivers
		existingUser, err := s.authStore.FindUserByReferralCode(ctx, code)
		if err != nil {
			return "", err
		}
		if existingUser != nil {
			continue
		}

		existingDriver, err := s.authStore.FindDriverByReferralCode(ctx, code)
		if err != nil {
			return "", err
		}
		if existingDriver != nil {
			continue
		}

		return code, nil
	}
	return "", fmt.Errorf("failed to generate unique referral code after %d attempts", maxAttempts)
}

// GetOrCreateUserCode ensures a user has a personal referral code.
// If the user already has one, it returns it. Otherwise, it generates and stores one.
func (s *Service) GetOrCreateUserCode(ctx context.Context, userID primitive.ObjectID) (string, error) {
	user, err := s.authStore.FindUserByID(ctx, userID)
	if err != nil || user == nil {
		return "", fmt.Errorf("user not found")
	}

	if user.MyReferralCode != "" {
		return user.MyReferralCode, nil
	}

	code, err := s.GenerateReferralCode(ctx)
	if err != nil {
		return "", err
	}

	if err := s.authStore.SetUserReferralCode(ctx, userID, code); err != nil {
		return "", err
	}
	return code, nil
}

// GetOrCreateDriverCode ensures a driver has a personal referral code.
func (s *Service) GetOrCreateDriverCode(ctx context.Context, driverID primitive.ObjectID) (string, error) {
	driver, err := s.authStore.FindDriverByID(ctx, driverID)
	if err != nil || driver == nil {
		return "", fmt.Errorf("driver not found")
	}

	if driver.MyReferralCode != "" {
		return driver.MyReferralCode, nil
	}

	code, err := s.GenerateReferralCode(ctx)
	if err != nil {
		return "", err
	}

	if err := s.authStore.SetDriverReferralCode(ctx, driverID, code); err != nil {
		return "", err
	}
	return code, nil
}

// ProcessSignupReferral handles the referral logic when a new user or driver signs up with a code.
// It validates the code, determines the referral type, creates a record, and credits if rides_required=0.
func (s *Service) ProcessSignupReferral(ctx context.Context, refereeID, refereeRole, code string) error {
	if code == "" {
		return nil
	}

	code = strings.ToUpper(strings.TrimSpace(code))

	// Find who owns this code — could be a user or a driver
	var referrerID string
	var referrerRole string

	user, err := s.authStore.FindUserByReferralCode(ctx, code)
	if err != nil {
		return err
	}
	if user != nil {
		referrerID = user.ID.Hex()
		referrerRole = "user"
	} else {
		driver, err := s.authStore.FindDriverByReferralCode(ctx, code)
		if err != nil {
			return err
		}
		if driver != nil {
			referrerID = driver.ID.Hex()
			referrerRole = "driver"
		}
	}

	if referrerID == "" {
		return fmt.Errorf("invalid referral code")
	}

	// Don't allow self-referral
	if referrerID == refereeID {
		return fmt.Errorf("cannot use your own referral code")
	}

	// Determine referral type
	refType := referrerRole + "_to_" + refereeRole
	if !ValidTypes[refType] {
		return fmt.Errorf("invalid referral type: %s", refType)
	}

	// Load config for this type
	cfg, err := s.store.GetConfigByType(ctx, refType)
	if err != nil {
		return err
	}
	if cfg == nil || !cfg.Enabled {
		logger.Log.Warn().Str("type", refType).Msg("Referral type not configured or disabled, skipping")
		return nil
	}

	// Create the referral record
	rec := &Record{
		Type:           refType,
		ReferrerID:     referrerID,
		ReferrerRole:   referrerRole,
		RefereeID:      refereeID,
		RefereeRole:    refereeRole,
		Code:           code,
		RidesRequired:  cfg.RidesRequired,
		RidesDone:      0,
		ReferrerAmount: cfg.ReferrerAmount,
		RefereeAmount:  cfg.NewUserAmount,
	}

	if err := s.store.CreateRecord(ctx, rec); err != nil {
		return err
	}

	logger.Log.Info().
		Str("type", refType).
		Str("referrer", referrerID).
		Str("referee", refereeID).
		Int("rides_required", cfg.RidesRequired).
		Msg("Referral record created")

	// If rides_required is 0, credit immediately
	if cfg.RidesRequired == 0 {
		s.creditReferrer(ctx, rec)
		s.creditReferee(ctx, rec)
	} else {
		// Credit the referee immediately (new user gets their bonus at signup)
		if rec.RefereeAmount > 0 {
			s.creditReferee(ctx, rec)
		}
	}

	return nil
}

// ProcessRideCompletion checks for pending referral records after a ride is completed.
// It increments ride counts and credits the referrer when the threshold is met.
func (s *Service) ProcessRideCompletion(ctx context.Context, userID, driverID string) {
	// Check if the user (as referee) has any pending referrals
	s.checkAndCreditForRide(ctx, userID, "user")

	// Check if the driver (as referee) has any pending referrals
	s.checkAndCreditForRide(ctx, driverID, "driver")
}

// checkAndCreditForRide processes ride completion for a specific entity (user or driver).
func (s *Service) checkAndCreditForRide(ctx context.Context, entityID, role string) {
	pending, err := s.store.FindPendingByReferee(ctx, entityID, role)
	if err != nil {
		logger.Log.Error().Err(err).Str("entity_id", entityID).Str("role", role).Msg("Failed to find pending referrals")
		return
	}

	for _, rec := range pending {
		updated, err := s.store.IncrementRidesDone(ctx, rec.ID)
		if err != nil {
			logger.Log.Error().Err(err).Str("record_id", rec.ID.Hex()).Msg("Failed to increment rides_done")
			continue
		}

		logger.Log.Info().
			Str("record_id", rec.ID.Hex()).
			Int("rides_done", updated.RidesDone).
			Int("rides_required", updated.RidesRequired).
			Msg("Referral ride count incremented")

		// Check if threshold is now met
		if updated.RidesDone >= updated.RidesRequired && !updated.ReferrerCredited {
			s.creditReferrer(ctx, updated)
		}
	}
}

// creditReferrer credits the referrer based on their role.
func (s *Service) creditReferrer(ctx context.Context, rec *Record) {
	if rec.ReferrerAmount <= 0 {
		_ = s.store.MarkReferrerCredited(ctx, rec.ID)
		return
	}

	var reason string
	if rec.RidesRequired == 0 {
		reason = "signup_referral"
	} else {
		reason = "ride_threshold_met"
	}

	switch rec.ReferrerRole {
	case "driver":
		// Credit driver wallet
		objID, err := primitive.ObjectIDFromHex(rec.ReferrerID)
		if err == nil {
			if err := s.walletStore.UpdateWalletBalance(ctx, objID, rec.ReferrerAmount); err != nil {
				logger.Log.Error().Err(err).Str("driver_id", rec.ReferrerID).Float64("amount", rec.ReferrerAmount).Msg("Failed to credit referrer driver wallet")
				return
			}
		}
	case "user":
		// Credit user via offers collection
		desc := fmt.Sprintf("Referral bonus: ₹%.0f credit", rec.ReferrerAmount)
		userOffer := &offer.Offer{
			Description: desc,
			UserID:      &rec.ReferrerID,
			OfferAmount: &rec.ReferrerAmount,
		}
		if err := s.offerStore.Create(ctx, userOffer); err != nil {
			logger.Log.Error().Err(err).Str("user_id", rec.ReferrerID).Float64("amount", rec.ReferrerAmount).Msg("Failed to credit referrer user offer")
			return
		}
	}

	_ = s.store.MarkReferrerCredited(ctx, rec.ID)

	logger.Log.Info().
		Str("referrer_id", rec.ReferrerID).
		Str("referrer_role", rec.ReferrerRole).
		Float64("amount", rec.ReferrerAmount).
		Str("reason", reason).
		Msg("Referrer credited")

	// Publish event for FCM push notification
	s.eventBus.PublishEvent(eventbus.ChannelReferralCredited, eventbus.ReferralCreditedPayload{
		RecordID:      rec.ID.Hex(),
		RecipientID:   rec.ReferrerID,
		RecipientRole: rec.ReferrerRole,
		Amount:        rec.ReferrerAmount,
		Reason:        reason,
	})
}

// creditReferee credits the referee (new user/driver) based on their role.
func (s *Service) creditReferee(ctx context.Context, rec *Record) {
	if rec.RefereeAmount <= 0 {
		_ = s.store.MarkRefereeCredited(ctx, rec.ID)
		return
	}

	switch rec.RefereeRole {
	case "driver":
		// Credit driver wallet
		objID, err := primitive.ObjectIDFromHex(rec.RefereeID)
		if err == nil {
			if err := s.walletStore.UpdateWalletBalance(ctx, objID, rec.RefereeAmount); err != nil {
				logger.Log.Error().Err(err).Str("driver_id", rec.RefereeID).Float64("amount", rec.RefereeAmount).Msg("Failed to credit referee driver wallet")
				return
			}
		}
	case "user":
		// Credit user via offers collection
		desc := fmt.Sprintf("Welcome bonus: ₹%.0f referral credit", rec.RefereeAmount)
		userOffer := &offer.Offer{
			Description: desc,
			UserID:      &rec.RefereeID,
			OfferAmount: &rec.RefereeAmount,
		}
		if err := s.offerStore.Create(ctx, userOffer); err != nil {
			logger.Log.Error().Err(err).Str("user_id", rec.RefereeID).Float64("amount", rec.RefereeAmount).Msg("Failed to credit referee user offer")
			return
		}
	}

	_ = s.store.MarkRefereeCredited(ctx, rec.ID)

	logger.Log.Info().
		Str("referee_id", rec.RefereeID).
		Str("referee_role", rec.RefereeRole).
		Float64("amount", rec.RefereeAmount).
		Msg("Referee credited")

	// Publish event for FCM push notification
	s.eventBus.PublishEvent(eventbus.ChannelReferralCredited, eventbus.ReferralCreditedPayload{
		RecordID:      rec.ID.Hex(),
		RecipientID:   rec.RefereeID,
		RecipientRole: rec.RefereeRole,
		Amount:        rec.RefereeAmount,
		Reason:        "welcome_bonus",
	})
}

// GetRewards builds the rewards response for a user or driver.
func (s *Service) GetRewards(ctx context.Context, entityID, role string) (*RewardsResponse, error) {
	// Get the referral code
	objID, err := primitive.ObjectIDFromHex(entityID)
	if err != nil {
		return nil, err
	}

	var myCode string
	switch role {
	case "user":
		myCode, err = s.GetOrCreateUserCode(ctx, objID)
	case "driver":
		myCode, err = s.GetOrCreateDriverCode(ctx, objID)
	default:
		return nil, fmt.Errorf("invalid role: %s", role)
	}
	if err != nil {
		return nil, err
	}

	// Get all records where this entity is the referrer
	referrerRecords, err := s.store.ListByReferrer(ctx, entityID)
	if err != nil {
		return nil, err
	}

	var totalEarned float64
	var availableCredit float64
	var summaries []ReferralSummary

	for _, rec := range referrerRecords {
		earned := 0.0
		pending := true
		if rec.ReferrerCredited {
			earned = rec.ReferrerAmount
			totalEarned += earned
			pending = false
		}

		// Look up referee name
		refName := "User"
		if rec.RefereeRole == "driver" {
			refName = "Driver"
		}

		summaries = append(summaries, ReferralSummary{
			RefereeName:   refName,
			RefereeRole:   rec.RefereeRole,
			RidesRequired: rec.RidesRequired,
			RidesDone:     rec.RidesDone,
			AmountEarned:  earned,
			Pending:       pending,
		})
	}

	// Also check records where this entity is the referee (to count credits received)
	refereeRecords, err := s.store.ListByReferee(ctx, entityID)
	if err != nil {
		return nil, err
	}
	for _, rec := range refereeRecords {
		if rec.RefereeCredited && rec.RefereeAmount > 0 {
			totalEarned += rec.RefereeAmount
		}
	}

	// For users, calculate available credit from offers
	if role == "user" {
		// The available credit comes from user-specific offers created by the referral system
		// This is a simplified calculation — the actual credit is applied at booking time
		availableCredit = 0
		for _, rec := range refereeRecords {
			if rec.RefereeCredited && rec.RefereeAmount > 0 {
				availableCredit += rec.RefereeAmount
			}
		}
		for _, rec := range referrerRecords {
			if rec.ReferrerCredited && rec.ReferrerAmount > 0 {
				availableCredit += rec.ReferrerAmount
			}
		}
	}

	// Generate promo messages based on enabled configs
	var promos []string
	configs, _ := s.store.ListConfigs(ctx)
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		switch {
		case role == "user" && cfg.Type == "user_to_user" && cfg.ReferrerAmount > 0:
			promos = append(promos, fmt.Sprintf("Refer a friend and earn ₹%.0f!", cfg.ReferrerAmount))
		case role == "user" && cfg.Type == "user_to_driver" && cfg.ReferrerAmount > 0:
			promos = append(promos, fmt.Sprintf("Refer a driver and earn ₹%.0f!", cfg.ReferrerAmount))
		case role == "driver" && cfg.Type == "driver_to_user" && cfg.ReferrerAmount > 0:
			promos = append(promos, fmt.Sprintf("Refer a user and earn ₹%.0f!", cfg.ReferrerAmount))
		case role == "driver" && cfg.Type == "driver_to_driver" && cfg.ReferrerAmount > 0:
			promos = append(promos, fmt.Sprintf("Refer a driver and earn ₹%.0f!", cfg.ReferrerAmount))
		}
	}

	if summaries == nil {
		summaries = []ReferralSummary{}
	}
	if promos == nil {
		promos = []string{}
	}

	return &RewardsResponse{
		MyReferralCode:  myCode,
		AvailableCredit: availableCredit,
		TotalEarned:     totalEarned,
		Referrals:       summaries,
		Promos:          promos,
	}, nil
}

// randomCode generates a random string of the given length from the code alphabet.
func randomCode(length int) (string, error) {
	max := big.NewInt(int64(len(codeAlphabet)))
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = codeAlphabet[n.Int64()]
	}
	return string(b), nil
}
