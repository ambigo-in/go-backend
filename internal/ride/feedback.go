package ride

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type Feedback struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID    string             `bson:"user_id" json:"user_id"`
	DriverID  string             `bson:"driver_id" json:"driver_id"`
	RideID    string             `bson:"ride_id" json:"ride_id"`
	Rating    float64            `bson:"rating" json:"rating" validate:"required,min=1,max=5"`
	Content   string             `bson:"content" json:"content"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
}

type FeedbackStore struct {
	feedback *mongo.Collection
}

func NewFeedbackStore(db *mongo.Database) *FeedbackStore {
	return &FeedbackStore{
		feedback: db.Collection("feedback"),
	}
}

func (s *FeedbackStore) InsertFeedback(ctx context.Context, f *Feedback) error {
	f.ID = primitive.NewObjectID()
	f.CreatedAt = time.Now()
	_, err := s.feedback.InsertOne(ctx, f)
	return err
}

func (s *FeedbackStore) ListAllFeedback(ctx context.Context) ([]Feedback, error) {
	cursor, err := s.feedback.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Feedback
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Feedback{}
	}
	return list, nil
}

func (s *FeedbackStore) ListFeedback(ctx context.Context, driverID string) ([]Feedback, error) {
	cursor, err := s.feedback.Find(ctx, bson.M{"driver_id": driverID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Feedback
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Feedback{}
	}
	return list, nil
}
