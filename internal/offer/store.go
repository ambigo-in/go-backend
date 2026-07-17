package offer

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type Store struct {
	collection *mongo.Collection
}

func NewStore(db *mongo.Database) *Store {
	return &Store{
		collection: db.Collection("offers"),
	}
}

func (s *Store) Create(ctx context.Context, o *Offer) error {
	o.ID = primitive.NewObjectID()
	_, err := s.collection.InsertOne(ctx, o)
	return err
}

func (s *Store) List(ctx context.Context) ([]Offer, error) {
	cursor, err := s.collection.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Offer
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Offer{}
	}
	return list, nil
}

func (s *Store) GetByID(ctx context.Context, id primitive.ObjectID) (*Offer, error) {
	var o Offer
	err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&o)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

func (s *Store) Delete(ctx context.Context, id primitive.ObjectID) error {
	_, err := s.collection.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *Store) FindByUserID(ctx context.Context, userID string) ([]Offer, error) {
	cursor, err := s.collection.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Offer
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Offer{}
	}
	return list, nil
}
