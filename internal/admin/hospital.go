package admin

import (
	"context"

	"ambigo-backend/internal/translation"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type GeoJSON struct {
	Type        string    `bson:"type" json:"type"`
	Coordinates []float64 `bson:"coordinates" json:"coordinates"`
}

type Timing struct {
	Start string `bson:"start" json:"start"`
	End   string `bson:"end" json:"end"`
}

type Hospital struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name        translation.Map    `bson:"name" json:"name"`
	Address     translation.Map    `bson:"address" json:"address"`
	City        translation.Map    `bson:"city" json:"city"`
	Location   GeoJSON            `bson:"location" json:"location"`
	Timing     *Timing            `bson:"timing,omitempty" json:"timing,omitempty"`
	AlwaysOpen bool               `bson:"always_open" json:"always_open"`
	Services   []string           `bson:"services" json:"services"`
}

type HospitalStore struct {
	hospitals *mongo.Collection
}

func NewHospitalStore(db *mongo.Database) *HospitalStore {
	return &HospitalStore{
		hospitals: db.Collection("hospitals"),
	}
}

func (s *HospitalStore) CreateHospital(ctx context.Context, h *Hospital) error {
	h.ID = primitive.NewObjectID()
	_, err := s.hospitals.InsertOne(ctx, h)
	return err
}

func (s *HospitalStore) ListHospitals(ctx context.Context) ([]Hospital, error) {
	cursor, err := s.hospitals.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []Hospital
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Hospital{}
	}
	return list, nil
}

func (s *HospitalStore) UpdateHospital(ctx context.Context, h *Hospital) error {
	filter := bson.M{"_id": h.ID}
	update := bson.M{"$set": h}
	_, err := s.hospitals.UpdateOne(ctx, filter, update)
	return err
}

func (s *HospitalStore) DeleteHospital(ctx context.Context, id primitive.ObjectID) error {
	_, err := s.hospitals.DeleteOne(ctx, bson.M{"_id": id})
	return err
}
