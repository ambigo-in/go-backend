package admin

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type CounterStore struct {
	counters *mongo.Collection
}

func NewCounterStore(db *mongo.Database) *CounterStore {
	return &CounterStore{
		counters: db.Collection("counters"),
	}
}

func (s *CounterStore) IncrementCounter(ctx context.Context, id string) error {
	filter := bson.M{"_id": id}
	update := bson.M{"$inc": bson.M{"value": 1}}
	opts := options.Update().SetUpsert(true)
	_, err := s.counters.UpdateOne(ctx, filter, update, opts)
	return err
}

func (s *CounterStore) GetCounter(ctx context.Context, id string) (int, error) {
	filter := bson.M{"_id": id}
	var result struct {
		Value int `bson:"value"`
	}
	err := s.counters.FindOne(ctx, filter).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return 0, nil
		}
		return 0, err
	}
	return result.Value, nil
}
