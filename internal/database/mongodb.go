package database

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoDB represents a MongoDB connection
type MongoDB struct {
	client   *mongo.Client
	database *mongo.Database
}

// UserData represents a user data document in MongoDB
type UserData struct {
	ID         primitive.ObjectID  `bson:"_id,omitempty" json:"id"`
	UserID     string              `bson:"user_id" json:"user_id"`
	VectorID   string              `bson:"vector_id" json:"vector_id"`
	DataType   string              `bson:"data_type" json:"data_type"`
	DataValue  string              `bson:"data_value" json:"data_value"`
	ParentID   *primitive.ObjectID `bson:"parent_id,omitempty" json:"parent_id,omitempty"`
	ChunkIndex int                 `bson:"chunk_index" json:"chunk_index"`
	CreatedAt  time.Time           `bson:"created_at" json:"created_at"`
}

// NewMongoDB creates a new MongoDB connection
func NewMongoDB(connectionString string) (*MongoDB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connectionString))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Ping the database to verify connection
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	// Create indexes
	database := client.Database("forgetai")
	collection := database.Collection("user_data")

	_, err = collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().SetBackground(true),
		},
		{
			Keys:    bson.D{{Key: "vector_id", Value: 1}},
			Options: options.Index().SetBackground(true).SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "parent_id", Value: 1}},
			Options: options.Index().SetBackground(true).SetSparse(true),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create indexes: %w", err)
	}

	fmt.Println("Successfully connected to MongoDB")

	return &MongoDB{
		client:   client,
		database: database,
	}, nil
}

// Close closes the MongoDB connection
func (m *MongoDB) Close(ctx context.Context) error {
	return m.client.Disconnect(ctx)
}

// CreateUserData creates a new user data document
func (m *MongoDB) CreateUserData(ctx context.Context, userData *UserData) (*UserData, error) {
	if userData.CreatedAt.IsZero() {
		userData.CreatedAt = time.Now()
	}

	result, err := m.database.Collection("user_data").InsertOne(ctx, userData)
	if err != nil {
		return nil, err
	}

	userData.ID = result.InsertedID.(primitive.ObjectID)
	return userData, nil
}

// GetUserDataByID gets a user data document by ID
func (m *MongoDB) GetUserDataByID(ctx context.Context, id string) (*UserData, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid object ID: %w", err)
	}

	var userData UserData
	err = m.database.Collection("user_data").FindOne(ctx, bson.M{"_id": objID}).Decode(&userData)
	if err != nil {
		return nil, err
	}

	return &userData, nil
}

// GetAllUserData gets all user data documents for a user (excluding chunks)
func (m *MongoDB) GetAllUserData(ctx context.Context, userID string) ([]*UserData, error) {
	cursor, err := m.database.Collection("user_data").Find(
		ctx,
		bson.M{
			"user_id":   userID,
			"parent_id": bson.M{"$exists": false},
		},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var items []*UserData
	if err := cursor.All(ctx, &items); err != nil {
		return nil, err
	}

	return items, nil
}

// GetUserDataByType gets user data documents by type
func (m *MongoDB) GetUserDataByType(ctx context.Context, userID, dataType string) ([]*UserData, error) {
	cursor, err := m.database.Collection("user_data").Find(
		ctx,
		bson.M{
			"user_id":   userID,
			"data_type": dataType,
			"parent_id": bson.M{"$exists": false},
		},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var items []*UserData
	if err := cursor.All(ctx, &items); err != nil {
		return nil, err
	}

	return items, nil
}

// DeleteUserData deletes a user data document
func (m *MongoDB) DeleteUserData(ctx context.Context, id, userID string) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid object ID: %w", err)
	}

	result, err := m.database.Collection("user_data").DeleteOne(ctx, bson.M{
		"_id":     objID,
		"user_id": userID,
	})
	if err != nil {
		return err
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("no document found with ID %s for user %s", id, userID)
	}

	return nil
}

// GetPDFChunks gets all chunks for a PDF
func (m *MongoDB) GetPDFChunks(ctx context.Context, parentID string) ([]*UserData, error) {
	objID, err := primitive.ObjectIDFromHex(parentID)
	if err != nil {
		return nil, fmt.Errorf("invalid object ID: %w", err)
	}

	cursor, err := m.database.Collection("user_data").Find(
		ctx,
		bson.M{"parent_id": objID},
		options.Find().SetSort(bson.D{{Key: "chunk_index", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var items []*UserData
	if err := cursor.All(ctx, &items); err != nil {
		return nil, err
	}

	return items, nil
}

// DeletePDFWithChunks deletes a PDF and all its chunks
func (m *MongoDB) DeletePDFWithChunks(ctx context.Context, id, userID string) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid object ID: %w", err)
	}

	// Start a session
	session, err := m.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	// Run transaction
	err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
		// Delete the chunks first
		_, err := m.database.Collection("user_data").DeleteMany(sc, bson.M{
			"parent_id": objID,
			"user_id":   userID,
		})
		if err != nil {
			return err
		}

		// Delete the parent
		result, err := m.database.Collection("user_data").DeleteOne(sc, bson.M{
			"_id":     objID,
			"user_id": userID,
		})
		if err != nil {
			return err
		}

		if result.DeletedCount == 0 {
			return fmt.Errorf("no document found with ID %s for user %s", id, userID)
		}

		return nil
	})

	return err
}

// GetVectorIDByDataID gets the vector ID for a data document
func (m *MongoDB) GetVectorIDByDataID(ctx context.Context, id string) (string, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return "", fmt.Errorf("invalid object ID: %w", err)
	}

	var result struct {
		VectorID string `bson:"vector_id"`
	}

	err = m.database.Collection("user_data").FindOne(
		ctx,
		bson.M{"_id": objID},
		options.FindOne().SetProjection(bson.M{"vector_id": 1, "_id": 0}),
	).Decode(&result)

	return result.VectorID, err
}
