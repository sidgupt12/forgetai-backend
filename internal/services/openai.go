package services

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

// OpenAIService handles interactions with the OpenAI API
type OpenAIService struct {
	client *openai.Client
}

// NewOpenAIService creates a new OpenAI service
func NewOpenAIService(apiKey string) *OpenAIService {
	return &OpenAIService{
		client: openai.NewClient(apiKey),
	}
}

// GetEmbedding generates an embedding for the given text
func (s *OpenAIService) GetEmbedding(text string) ([]float32, error) {
	fmt.Printf("Generating embedding for text: %s\n", text)
	req := openai.EmbeddingRequest{
		Input: []string{text},
		Model: "text-embedding-3-small",
	}
	resp, err := s.client.CreateEmbeddings(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("no embedding data returned")
	}
	fmt.Printf("Generated embedding of length: %d\n", len(resp.Data[0].Embedding))
	return resp.Data[0].Embedding, nil
}

// GetChatCompletion generates a chat completion for the given messages
func (s *OpenAIService) GetChatCompletion(messages []openai.ChatCompletionMessage) (string, error) {
	resp, err := s.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:    "gpt-4o-mini",
			Messages: messages,
		},
	)
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Message.Content, nil
}
