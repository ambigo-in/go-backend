package admin

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type Store struct {
	ambulanceTypes *mongo.Collection
	admins         *mongo.Collection
}

func NewStore(dataDB, usersDB *mongo.Database) *Store {
	return &Store{
		ambulanceTypes: dataDB.Collection("ambulance_type"),
		admins:         usersDB.Collection("admin"),
	}
}

func (s *Store) CreateAmbulanceType(ctx context.Context, amb *AmbulanceType) error {
	amb.ID = primitive.NewObjectID()
	_, err := s.ambulanceTypes.InsertOne(ctx, amb)
	return err
}

func (s *Store) ListAmbulanceTypes(ctx context.Context) ([]AmbulanceType, error) {
	cursor, err := s.ambulanceTypes.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var list []AmbulanceType
	if err = cursor.All(ctx, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []AmbulanceType{}
	}
	return list, nil
}

func (s *Store) GetAmbulanceTypeByID(ctx context.Context, idStr string) (*AmbulanceType, error) {
	objID, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return nil, err
	}

	var amb AmbulanceType
	err = s.ambulanceTypes.FindOne(ctx, bson.M{"_id": objID}).Decode(&amb)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &amb, nil
}

func (s *Store) DeleteAmbulanceType(ctx context.Context, id primitive.ObjectID) error {
	_, err := s.ambulanceTypes.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *Store) FindAdminByUsername(ctx context.Context, username string) (*Admin, error) {
	var admin Admin
	err := s.admins.FindOne(ctx, bson.M{"username": username}).Decode(&admin)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &admin, nil
}

func (s *Store) FindAdminByMobile(ctx context.Context, mobile string) (*Admin, error) {
	var admin Admin
	err := s.admins.FindOne(ctx, bson.M{"mobile": mobile}).Decode(&admin)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &admin, nil
}
