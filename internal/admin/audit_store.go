package admin

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type AuditStore struct {
	collection *mongo.Collection
}

func NewAuditStore(db *mongo.Database) *AuditStore {
	return &AuditStore{
		collection: db.Collection("audit_log"),
	}
}

type AuditEvent struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	EventType string             `bson:"event_type"`
	Channel   string             `bson:"channel"`
	Payload   string             `bson:"payload"`
	RequestID string             `bson:"request_id,omitempty"`
	CreatedAt time.Time          `bson:"created_at"`
}

func (s *AuditStore) InsertEvent(ctx context.Context, event *AuditEvent) error {
	event.ID = primitive.NewObjectID()
	event.CreatedAt = time.Now()
	_, err := s.collection.InsertOne(ctx, event)
	return err
}
