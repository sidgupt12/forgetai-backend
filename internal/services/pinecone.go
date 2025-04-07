package services

import (
	"context"
	"fmt"
	"time"

	"github.com/pinecone-io/go-pinecone/v3/pinecone"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/siddhantgupta/forgetai-backend/internal/models"
)

// PineconeService handles interactions with the Pinecone API
type PineconeService struct {
	client    *pinecone.Client
	indexHost string
}

// NewPineconeService creates a new Pinecone service
func NewPineconeService(apiKey, indexHost string) (*PineconeService, error) {
	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey: apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Pinecone client: %v", err)
	}

	return &PineconeService{
		client:    pc,
		indexHost: indexHost,
	}, nil
}

// UpsertVector inserts or updates a vector in Pinecone
func (s *PineconeService) UpsertVector(ctx context.Context, id string, embedding []float32, data models.Data) error {
	idxConnection, err := s.client.Index(pinecone.NewIndexConnParams{
		Host: s.indexHost,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to index: %v", err)
	}

	metadataMap := map[string]interface{}{
		"text":      data.Text,
		"user_id":   data.UserId,
		"type":      data.Selected_type,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	metadata, err := structpb.NewStruct(metadataMap)
	if err != nil {
		return fmt.Errorf("failed to create metadata struct: %v", err)
	}

	vector := &pinecone.Vector{
		Id:       id,
		Values:   &embedding,
		Metadata: metadata,
	}

	count, err := idxConnection.UpsertVectors(ctx, []*pinecone.Vector{vector})
	if err != nil {
		return fmt.Errorf("failed to upsert vector: %v", err)
	}

	fmt.Printf("Successfully upserted %d vector(s)!\n", count)
	return nil
}

// QueryVectors queries vectors in Pinecone
func (s *PineconeService) QueryVectors(ctx context.Context, userId string, embedding []float32) (*pinecone.QueryVectorsResponse, error) {
	idxConnection, err := s.client.Index(pinecone.NewIndexConnParams{
		Host: s.indexHost,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to index: %v", err)
	}

	filter, err := structpb.NewStruct(map[string]interface{}{
		"user_id": userId,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create filter: %v", err)
	}

	res, err := idxConnection.QueryByVectorValues(ctx, &pinecone.QueryByVectorValuesRequest{
		Vector:          embedding,
		TopK:            50,
		IncludeValues:   false,
		IncludeMetadata: true,
		MetadataFilter:  filter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query vectors: %v", err)
	}

	return res, nil
}
