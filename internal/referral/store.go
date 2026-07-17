package referral

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Store handles MongoDB operations for referral configs and records.
type Store struct {
	configs *mongo.Collection // Data DB → referral_config
	records *mongo.Collection // Records DB → referral_records
}

// NewStore creates a new referral Store.
// dataDB is the "Data" database (for configs), recordsDB is the "Records" database (for tracking).
func NewStore(dataDB, recordsDB *mongo.Database) *Store {
	return &Store{
		configs: dataDB.Collection("referral_config"),
		records: recordsDB.Collection("referral_records"),
	}
}

// ---- Config CRUD ----

// ListConfigs returns all referral type configurations.
func (s *Store) ListConfigs(ctx context.Context) ([]Config, error) {
	cursor, err := s.configs.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Config
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Config{}
	}
	return list, nil
}

// SaveConfigs upserts all configs by their "type" field.
// This replaces the full config set each time the admin saves.
func (s *Store) SaveConfigs(ctx context.Context, configs []Config) error {
	for _, cfg := range configs {
		filter := bson.M{"type": cfg.Type}
		update := bson.M{
			"$set": bson.M{
				"referrer_amount": cfg.ReferrerAmount,
				"new_user_amount": cfg.NewUserAmount,
				"rides_required":  cfg.RidesRequired,
				"enabled":         cfg.Enabled,
			},
			"$setOnInsert": bson.M{
				"type": cfg.Type,
			},
		}
		opts := options.Update().SetUpsert(true)
		if _, err := s.configs.UpdateOne(ctx, filter, update, opts); err != nil {
			return err
		}
	}
	return nil
}

// GetConfigByType returns the config for a specific referral type (e.g., "user_to_driver").
func (s *Store) GetConfigByType(ctx context.Context, refType string) (*Config, error) {
	var cfg Config
	err := s.configs.FindOne(ctx, bson.M{"type": refType}).Decode(&cfg)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &cfg, nil
}

// ---- Record CRUD ----

// CreateRecord inserts a new referral tracking record.
func (s *Store) CreateRecord(ctx context.Context, rec *Record) error {
	rec.ID = primitive.NewObjectID()
	rec.CreatedAt = time.Now()
	_, err := s.records.InsertOne(ctx, rec)
	return err
}

// FindPendingByReferee returns referral records for a referee where rides_done < rides_required
// and the referrer hasn't been credited yet.
func (s *Store) FindPendingByReferee(ctx context.Context, refereeID, refereeRole string) ([]Record, error) {
	filter := bson.M{
		"referee_id":        refereeID,
		"referee_role":      refereeRole,
		"referrer_credited": false,
		"$expr":             bson.M{"$lt": []string{"$rides_done", "$rides_required"}},
	}
	cursor, err := s.records.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Record
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindPendingByUser finds pending referral records where the given user is the referee.
func (s *Store) FindPendingByUser(ctx context.Context, userID string) ([]Record, error) {
	return s.FindPendingByReferee(ctx, userID, "user")
}

// FindPendingByDriver finds pending referral records where the given driver is the referee.
func (s *Store) FindPendingByDriver(ctx context.Context, driverID string) ([]Record, error) {
	return s.FindPendingByReferee(ctx, driverID, "driver")
}

// IncrementRidesDone atomically increments rides_done by 1 for a referral record.
// Returns the updated record.
func (s *Store) IncrementRidesDone(ctx context.Context, recordID primitive.ObjectID) (*Record, error) {
	filter := bson.M{"_id": recordID}
	update := bson.M{"$inc": bson.M{"rides_done": 1}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var rec Record
	err := s.records.FindOneAndUpdate(ctx, filter, update, opts).Decode(&rec)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// MarkReferrerCredited marks the referrer as credited for a referral record.
func (s *Store) MarkReferrerCredited(ctx context.Context, recordID primitive.ObjectID) error {
	now := time.Now()
	_, err := s.records.UpdateOne(ctx, bson.M{"_id": recordID}, bson.M{
		"$set": bson.M{"referrer_credited": true, "completed_at": now},
	})
	return err
}

// MarkRefereeCredited marks the referee as credited for a referral record.
func (s *Store) MarkRefereeCredited(ctx context.Context, recordID primitive.ObjectID) error {
	_, err := s.records.UpdateOne(ctx, bson.M{"_id": recordID}, bson.M{
		"$set": bson.M{"referee_credited": true},
	})
	return err
}

// GetRecordByID fetches a single referral record by ID.
func (s *Store) GetRecordByID(ctx context.Context, id primitive.ObjectID) (*Record, error) {
	var rec Record
	err := s.records.FindOne(ctx, bson.M{"_id": id}).Decode(&rec)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

// ListByReferrer returns all referral records where referrer_id matches.
func (s *Store) ListByReferrer(ctx context.Context, referrerID string) ([]Record, error) {
	cursor, err := s.records.Find(ctx, bson.M{"referrer_id": referrerID}, options.Find().SetSort(bson.M{"created_at": -1}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Record
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Record{}
	}
	return list, nil
}

// ListByReferee returns all referral records where referee_id matches.
func (s *Store) ListByReferee(ctx context.Context, refereeID string) ([]Record, error) {
	cursor, err := s.records.Find(ctx, bson.M{"referee_id": refereeID}, options.Find().SetSort(bson.M{"created_at": -1}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Record
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Record{}
	}
	return list, nil
}
