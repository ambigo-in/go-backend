package referral

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/ids"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/offer"
	"ambigo-backend/internal/payment"

	"github.com/jackc/pgx/v5"
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
func (s *Service) GetOrCreateUserCode(ctx context.Context, userID string) (string, error) {
	if !ids.IsValid(userID) {
		return "", fmt.Errorf("invalid user id: %s", userID)
	}
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
func (s *Service) GetOrCreateDriverCode(ctx context.Context, driverID string) (string, error) {
	if !ids.IsValid(driverID) {
		return "", fmt.Errorf("invalid driver id: %s", driverID)
	}
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
		referrerID = user.ID
		referrerRole = "user"
	} else {
		driver, err := s.authStore.FindDriverByReferralCode(ctx, code)
		if err != nil {
			return err
		}
		if driver != nil {
			referrerID = driver.ID
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
		// Count only while below threshold; at-threshold rows are retries of
		// a failed credit and must not inflate the counter.
		updated := &rec
		if rec.RidesDone < rec.RidesRequired {
			updated, err = s.store.IncrementRidesDone(ctx, rec.ID)
			if err != nil {
				logger.Log.Error().Err(err).Str("record_id", rec.ID).Msg("Failed to increment rides_done")
				continue
			}

			logger.Log.Info().
				Str("record_id", rec.ID).
				Int("rides_done", updated.RidesDone).
				Int("rides_required", updated.RidesRequired).
				Msg("Referral ride count incremented")
		}

		// Check if threshold is now met
		if updated.RidesDone >= updated.RidesRequired && !updated.ReferrerCredited {
			s.creditReferrer(ctx, updated)
		}
	}
}

// creditReferrer credits the referrer based on their role.
// Atomicity: claim-flag + wallet credit + ledger row commit in ONE
// transaction. Concurrent ride completions race on ClaimReferrerCredit;
// exactly one wins and credits, losers return silently (idempotent).
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
		// Credit driver wallet — string UUID, validated via ids.IsValid
		if !ids.IsValid(rec.ReferrerID) {
			logger.Log.Error().Str("driver_id", rec.ReferrerID).Msg("Invalid referrer driver ID")
			return
		}
		err := WithTx(ctx, s.store.Pool(), func(tx pgx.Tx) error {
			rTx := s.store.WithTx(tx)
			wTx := s.walletStore.WithTx(tx)
			claimed, err := rTx.ClaimReferrerCredit(ctx, rec.ID)
			if err != nil {
				return err
			}
			if !claimed {
				return nil // lost the race; winner credited already
			}
			if err := wTx.UpdateWalletBalance(ctx, rec.ReferrerID, rec.ReferrerAmount); err != nil {
				return err
			}
			bal, berr := wTx.GetBalance(ctx, rec.ReferrerID)
			if berr != nil {
				return berr
			}
			return wTx.InsertLedgerEntry(ctx, &payment.WalletTransaction{
				DriverID:    rec.ReferrerID,
				Amount:      rec.ReferrerAmount,
				Direction:   "credit",
				TxnType:     "referral_credit",
				ReferenceID: rec.ID,
				Status:      "success",
				BalanceAfter: &bal,
			})
		})
		if err != nil {
			logger.Log.Error().Err(err).Str("driver_id", rec.ReferrerID).Float64("amount", rec.ReferrerAmount).Msg("Failed to credit referrer driver wallet")
			return
		}
	case "user":
		// Claim-first (atomic CAS): exactly one concurrent worker wins.
		// Then create the offer. If creation fails, the miss is parked for
		// ops (manual re-issue) — strictly better than the old order, which
		// double-credited on every race.
		claimed, cerr := s.store.ClaimReferrerCredit(ctx, rec.ID)
		if cerr != nil {
			logger.Log.Error().Err(cerr).Str("record_id", rec.ID).Msg("Failed to claim referrer credit")
			return
		}
		if !claimed {
			return // lost the race; winner is creating (or created) the offer
		}
		// Credit user via offers collection
		desc := fmt.Sprintf("Referral bonus: Rs.%.0f credit", rec.ReferrerAmount)
		userOffer := &offer.Offer{
			Description: desc,
			UserID:      &rec.ReferrerID,
			OfferAmount: &rec.ReferrerAmount,
		}
		if err := s.offerStore.Create(ctx, userOffer); err != nil {
			logger.Log.Error().Err(err).Str("user_id", rec.ReferrerID).Float64("amount", rec.ReferrerAmount).Str("record_id", rec.ID).Msg("PARKED FOR OPS: referrer credit claimed but offer not created — re-issue manually")
			return
		}
	}

	logger.Log.Info().
		Str("referrer_id", rec.ReferrerID).
		Str("referrer_role", rec.ReferrerRole).
		Float64("amount", rec.ReferrerAmount).
		Str("reason", reason).
		Msg("Referrer credited")

	// Publish event for FCM push notification
	s.eventBus.PublishEvent(eventbus.ChannelReferralCredited, eventbus.ReferralCreditedPayload{
		RecordID:      rec.ID,
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
		// Credit driver wallet — claim + credit + ledger in one Tx.
		if !ids.IsValid(rec.RefereeID) {
			logger.Log.Error().Str("driver_id", rec.RefereeID).Msg("Invalid referee driver ID")
			return
		}
		err := WithTx(ctx, s.store.Pool(), func(tx pgx.Tx) error {
			rTx := s.store.WithTx(tx)
			wTx := s.walletStore.WithTx(tx)
			claimed, err := rTx.ClaimRefereeCredit(ctx, rec.ID)
			if err != nil {
				return err
			}
			if !claimed {
				return nil
			}
			if err := wTx.UpdateWalletBalance(ctx, rec.RefereeID, rec.RefereeAmount); err != nil {
				return err
			}
			bal, berr := wTx.GetBalance(ctx, rec.RefereeID)
			if berr != nil {
				return berr
			}
			return wTx.InsertLedgerEntry(ctx, &payment.WalletTransaction{
				DriverID:    rec.RefereeID,
				Amount:      rec.RefereeAmount,
				Direction:   "credit",
				TxnType:     "referral_credit",
				ReferenceID: rec.ID,
				Status:      "success",
				BalanceAfter: &bal,
			})
		})
		if err != nil {
			logger.Log.Error().Err(err).Str("driver_id", rec.RefereeID).Float64("amount", rec.RefereeAmount).Msg("Failed to credit referee driver wallet")
			return
		}
	case "user":
		claimed, cerr := s.store.ClaimRefereeCredit(ctx, rec.ID)
		if cerr != nil {
			logger.Log.Error().Err(cerr).Str("record_id", rec.ID).Msg("Failed to claim referee credit")
			return
		}
		if !claimed {
			return
		}
		// Credit user via offers collection
		desc := fmt.Sprintf("Welcome bonus: Rs.%.0f referral credit", rec.RefereeAmount)
		userOffer := &offer.Offer{
			Description: desc,
			UserID:      &rec.RefereeID,
			OfferAmount: &rec.RefereeAmount,
		}
		if err := s.offerStore.Create(ctx, userOffer); err != nil {
			logger.Log.Error().Err(err).Str("user_id", rec.RefereeID).Float64("amount", rec.RefereeAmount).Str("record_id", rec.ID).Msg("PARKED FOR OPS: referee credit claimed but offer not created — re-issue manually")
			return
		}
	}

	logger.Log.Info().
		Str("referee_id", rec.RefereeID).
		Str("referee_role", rec.RefereeRole).
		Float64("amount", rec.RefereeAmount).
		Msg("Referee credited")

	// Publish event for FCM push notification
	s.eventBus.PublishEvent(eventbus.ChannelReferralCredited, eventbus.ReferralCreditedPayload{
		RecordID:      rec.ID,
		RecipientID:   rec.RefereeID,
		RecipientRole: rec.RefereeRole,
		Amount:        rec.RefereeAmount,
		Reason:        "welcome_bonus",
	})
}

// GetRewards builds the rewards response for a user or driver.
func (s *Service) GetRewards(ctx context.Context, entityID, role string) (*RewardsResponse, error) {
	// Validate entityID is a valid UUID string
	if !ids.IsValid(entityID) {
		return nil, fmt.Errorf("invalid id: %s", entityID)
	}

	var myCode string
	var err error
	switch role {
	case "user":
		myCode, err = s.GetOrCreateUserCode(ctx, entityID)
	case "driver":
		myCode, err = s.GetOrCreateDriverCode(ctx, entityID)
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

	// For users, calculate available credit from actual offers in DB
	if role == "user" {
		availableCredit = 0
		userOffers, err := s.offerStore.FindByUserID(ctx, entityID)
		if err == nil {
			for _, o := range userOffers {
				if o.OfferAmount != nil && *o.OfferAmount > 0 {
					availableCredit += *o.OfferAmount
				}
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

func (s *Service) ConsumeUserReferralCredit(ctx context.Context, userID string) (float64, error) {
	amount, claimed, err := s.offerStore.ClaimHighestByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	if !claimed {
		return 0, nil
	}

	logger.Log.Info().Str("user_id", userID).Float64("amount", amount).Msg("Referral credit consumed")
	return amount, nil
}

// RestoreUserReferralCredit compensates a consumed-then-failed ride: the
// discount value is re-issued so the user never loses credit for a ride that
// did not complete. Callers must invoke it on every failure path after a
// successful ConsumeUserReferralCredit.
func (s *Service) RestoreUserReferralCredit(ctx context.Context, userID string, amount float64) {
	if amount <= 0 {
		return
	}
	if err := s.offerStore.RestoreCredit(ctx, userID, amount, "Restored referral credit (ride not completed)"); err != nil {
		logger.Log.Error().Err(err).Str("user_id", userID).Float64("amount", amount).Msg("PARKED FOR OPS: failed to restore consumed referral credit")
	}
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
