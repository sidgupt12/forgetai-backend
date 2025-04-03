package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/pinecone-io/go-pinecone/v3/pinecone"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/protobuf/types/known/structpb"
)

// Data models
type Data struct {
	Selected_type string `json:"selected_type"`
	Text          string `json:"text"`
	UserId        string `json:"user_id"`
}

type QueryRequest struct {
	Text      string `json:"text" binding:"required"`
	UserId    string `json:"userId" binding:"required"`
	SessionId string `json:"sessionId"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatSession struct {
	Messages  []ChatMessage `json:"messages"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type QueryResult struct {
	VectorId  string    `json:"vector_id"`
	Score     float32   `json:"score"`
	Text      string    `json:"text"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
}

type QueryResponse struct {
	Message      string    `json:"message"`
	Answer       string    `json:"answer"`
	ContextText  string    `json:"context_text"`
	SessionId    string    `json:"session_id"`
	SessionCount int       `json:"session_count"`
	Timestamp    time.Time `json:"timestamp"`
}

type UpsertResponse struct {
	Message   string    `json:"message"`
	Text      string    `json:"text"`
	UserId    string    `json:"user_id"`
	Type      string    `json:"type"`
	VectorId  string    `json:"vector_id"`
	Timestamp time.Time `json:"timestamp"`
}

// Global variables
var sessions = make(map[string]ChatSession)

// Helper functions
func prettifyStruct(obj any) string {
	bytes, _ := json.MarshalIndent(obj, "", "  ")
	return string(bytes)
}

// OpenAI service
type OpenAIService struct {
	client *openai.Client
}

func NewOpenAIService() *OpenAIService {
	return &OpenAIService{
		client: openai.NewClient(os.Getenv("OPENAI_API_KEY")),
	}
}

func (s *OpenAIService) GetEmbedding(text string) ([]float32, error) {
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
	return resp.Data[0].Embedding, nil
}

func (s *OpenAIService) GetChatCompletion(messages []openai.ChatCompletionMessage) (string, error) {
	resp, err := s.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:    "gpt-4o",
			Messages: messages,
		},
	)
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Message.Content, nil
}

// Pinecone service
type PineconeService struct {
	client    *pinecone.Client
	indexHost string
}

func NewPineconeService() (*PineconeService, error) {
	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey: os.Getenv("PINECONE_API_KEY"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Pinecone client: %v", err)
	}

	return &PineconeService{
		client:    pc,
		indexHost: os.Getenv("PINECONE_INDEX_HOST"),
	}, nil
}

func (s *PineconeService) UpsertVector(ctx context.Context, id string, embedding []float32, data Data) error {
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
		TopK:            5,
		IncludeValues:   false,
		IncludeMetadata: true,
		MetadataFilter:  filter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query vectors: %v", err)
	}

	return res, nil
}

// Session management
func getOrCreateSession(sessionId, userId string) (string, *ChatSession) {
	if sessionId == "" {
		// Create a new session ID
		sessionId = fmt.Sprintf("%s-%s", userId, uuid.New().String())
	}

	session, exists := sessions[sessionId]
	if !exists {
		// Initialize a new session
		session = ChatSession{
			Messages:  []ChatMessage{},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		sessions[sessionId] = session
	}

	return sessionId, &session
}

func addMessageToSession(sessionId string, role, content string) {
	session, exists := sessions[sessionId]
	if !exists {
		return
	}

	// Add the new message
	session.Messages = append(session.Messages, ChatMessage{
		Role:    role,
		Content: content,
	})

	// Keep only the last 5 messages (last 5 turns = 10 messages if counting both user and assistant)
	if len(session.Messages) > 10 {
		session.Messages = session.Messages[len(session.Messages)-10:]
	}

	session.UpdatedAt = time.Now()
	sessions[sessionId] = session
}

func getSessionMessages(sessionId string) []openai.ChatCompletionMessage {
	session, exists := sessions[sessionId]
	if !exists {
		return []openai.ChatCompletionMessage{}
	}

	messages := []openai.ChatCompletionMessage{}
	for _, msg := range session.Messages {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return messages
}

// Handler setup
func setupRoutes(r *gin.Engine, openaiService *OpenAIService, pineconeService *PineconeService) {
	r.POST("/api/save", func(c *gin.Context) {
		var req Data
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		embedding, err := openaiService.GetEmbedding(req.Text)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
			return
		}

		vectorId := fmt.Sprintf("%s-%d", req.UserId, time.Now().UnixNano())

		err = pineconeService.UpsertVector(c.Request.Context(), vectorId, embedding, req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upsert to database: " + err.Error()})
			return
		}

		c.JSON(http.StatusOK, UpsertResponse{
			Message:   "Data saved successfully",
			Text:      req.Text,
			UserId:    req.UserId,
			Type:      req.Selected_type,
			VectorId:  vectorId,
			Timestamp: time.Now(),
		})
	})

	r.POST("/api/query", func(c *gin.Context) {
		var req QueryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		text := req.Text
		userId := req.UserId

		if text == "" || userId == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required parameters: text and userId"})
			return
		}

		// Get or create session
		sessionId, _ := getOrCreateSession(req.SessionId, userId)

		// Get embedding for the query
		embedding, err := openaiService.GetEmbedding(text)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
			return
		}

		// Search for relevant context in Pinecone
		res, err := pineconeService.QueryVectors(c.Request.Context(), userId, embedding)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query database: " + err.Error()})
			return
		}

		// Process the results
		// var contextText string
		// if len(res.Matches) > 0 {
		// 	topMatch := res.Matches[0]
		// 	metadata := topMatch.Vector.Metadata.AsMap()
		// 	contextText = metadata["text"].(string)
		// }
		contextText := ""
		if len(res.Matches) > 0 {
			for i, match := range res.Matches[:min(5, len(res.Matches))] { // Take up to 4
				metadata := match.Vector.Metadata.AsMap()
				contextText += fmt.Sprintf("Result %d: %s (Score: %.2f)\n", i+1, metadata["text"].(string), match.Score)
			}
		} else {
			contextText = "No relevant results found."
		}

		// Add user's query to the session
		addMessageToSession(sessionId, "user", text)

		// Prepare messages for OpenAI
		messages := getSessionMessages(sessionId)

		// Add system message with context if available
		systemPrompt := "You are like a second brain to the user. Answer the user's question as accurately as possible. And only give relevent responses saved by user no extra unnecessary information. But read all top results and create a message by extracting information from all and creating a message. Keep the response short and to the point."
		if contextText != "" {
			systemPrompt += " Use the following context to help answer the user's question: " + contextText
		}

		// Create the final chat messages with the system message at the beginning
		finalMessages := []openai.ChatCompletionMessage{
			{
				Role:    "system",
				Content: systemPrompt,
			},
		}
		finalMessages = append(finalMessages, messages...)

		// Get response from OpenAI
		response, err := openaiService.GetChatCompletion(finalMessages)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get AI response: " + err.Error()})
			return
		}

		// Add assistant's response to the session
		addMessageToSession(sessionId, "assistant", response)

		// Return the response
		c.JSON(http.StatusOK, QueryResponse{
			Message:      "Query successful",
			Answer:       response,
			ContextText:  contextText,
			SessionId:    sessionId,
			SessionCount: len(sessions[sessionId].Messages) / 2, // Count conversation turns
			Timestamp:    time.Now(),
		})
	})

	// Add endpoint to reset/clear a session
	r.POST("/api/reset-session", func(c *gin.Context) {
		var req struct {
			SessionId string `json:"sessionId" binding:"required"`
			UserId    string `json:"userId" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		// Create a new session ID
		newSessionId := fmt.Sprintf("%s-%s", req.UserId, uuid.New().String())

		// Initialize a new session
		sessions[newSessionId] = ChatSession{
			Messages:  []ChatMessage{},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		c.JSON(http.StatusOK, gin.H{
			"message":   "Session reset successfully",
			"sessionId": newSessionId,
		})
	})

	// Add endpoint to get session history
	r.GET("/api/session/:sessionId", func(c *gin.Context) {
		sessionId := c.Param("sessionId")

		session, exists := sessions[sessionId]
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"sessionId":    sessionId,
			"messages":     session.Messages,
			"messageCount": len(session.Messages),
			"createdAt":    session.CreatedAt,
			"updatedAt":    session.UpdatedAt,
		})
	})
}

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("Error loading .env file")
	}

	// Initialize services
	openaiService := NewOpenAIService()
	pineconeService, err := NewPineconeService()
	if err != nil {
		fmt.Printf("Failed to initialize Pinecone service: %v\n", err)
		return
	}

	// Setup Gin router
	r := gin.Default()

	// Setup routes
	setupRoutes(r, openaiService, pineconeService)

	// Start server
	fmt.Println("Server is running on port 8080")
	r.Run(":8080")
}
