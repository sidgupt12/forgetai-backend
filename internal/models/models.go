package models

import (
	"time"

	"github.com/sashabaranov/go-openai"
)

// Data represents user content to be stored
type Data struct {
	Selected_type string `json:"selected_type"`
	Text          string `json:"text"`
	UserId        string `json:"user_id"`
}

// QueryRequest represents a query request from the client
type QueryRequest struct {
	Text      string `json:"text" binding:"required"`
	UserId    string `json:"userId" binding:"required"`
	SessionId string `json:"sessionId"`
}

// ChatMessage represents a message in a chat session
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatSession represents a conversation session
type ChatSession struct {
	Messages  []ChatMessage `json:"messages"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// QueryResponse represents the response to a query request
type QueryResponse struct {
	Message      string    `json:"message"`
	Answer       string    `json:"answer"`
	ContextText  string    `json:"context_text"`
	SessionId    string    `json:"session_id"`
	SessionCount int       `json:"session_count"`
	Timestamp    time.Time `json:"timestamp"`
}

// UpsertResponse represents the response to an upsert request
type UpsertResponse struct {
	Message   string    `json:"message"`
	Text      string    `json:"text"`
	UserId    string    `json:"user_id"`
	Type      string    `json:"type"`
	VectorId  string    `json:"vector_id"`
	Timestamp time.Time `json:"timestamp"`
}

// ToOpenAIChatMessages converts internal ChatMessages to OpenAI format
func ToOpenAIChatMessages(messages []ChatMessage) []openai.ChatCompletionMessage {
	result := make([]openai.ChatCompletionMessage, len(messages))
	for i, msg := range messages {
		result[i] = openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}
	return result
}
