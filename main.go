package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/ledongthuc/pdf"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/pinecone-io/go-pinecone/v3/pinecone"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/protobuf/types/known/structpb"
)

// Data models - keeping your existing models
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

// ClerkAuth handles JWT verification with Clerk
type ClerkAuth struct {
	JWKSet     jwk.Set
	IssuerURL  string
	LastUpdate time.Time
}

// NewClerkAuth creates a new Clerk authenticator
func NewClerkAuth() (*ClerkAuth, error) {
	issuerURL := os.Getenv("CLERK_ISSUER_URL")
	if issuerURL == "" {
		return nil, fmt.Errorf("CLERK_ISSUER_URL environment variable not set")
	}

	auth := &ClerkAuth{
		IssuerURL: issuerURL,
	}

	// Fetch JWKs on initialization
	if err := auth.RefreshJWKs(); err != nil {
		return nil, err
	}

	return auth, nil
}

// RefreshJWKs fetches the latest JWKs from Clerk
func (c *ClerkAuth) RefreshJWKs() error {
	jwksURL := fmt.Sprintf("%s/.well-known/jwks.json", c.IssuerURL)
	set, err := jwk.Fetch(context.Background(), jwksURL)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKs: %v", err)
	}

	c.JWKSet = set
	c.LastUpdate = time.Now()
	return nil
}

// VerifyToken verifies a JWT token from Clerk
func (c *ClerkAuth) VerifyToken(tokenString string) (jwt.MapClaims, error) {
	// Check if JWKs need refreshing (every 24 hours)
	if time.Since(c.LastUpdate) > 24*time.Hour {
		if err := c.RefreshJWKs(); err != nil {
			// Continue with existing keys if refresh fails
			fmt.Printf("Warning: Failed to refresh JWKs: %v\n", err)
		}
	}

	// Parse the token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Validate the algorithm
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		// Get the key ID from the token header
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("kid header not found in token")
		}

		// Find the key with matching kid
		if key, found := c.JWKSet.LookupKeyID(kid); found {
			var rawKey interface{}
			if err := key.Raw(&rawKey); err != nil {
				return nil, fmt.Errorf("failed to get raw key: %v", err)
			}
			return rawKey, nil
		}

		return nil, fmt.Errorf("key with ID %s not found", kid)
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %v", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims format")
	}

	// // Verify the issuer
	// if !claims.VerifyIssuer(c.IssuerURL, true) {
	// 	return nil, fmt.Errorf("invalid issuer")
	// }

	// // Verify expiration
	// if !claims.VerifyExpiresAt(time.Now().Unix(), true) {
	// 	return nil, fmt.Errorf("token expired")
	// }

	issuer, ok := claims["iss"].(string)
	if !ok || issuer != c.IssuerURL {
		return nil, fmt.Errorf("invalid issuer")
	}

	exp, ok := claims["exp"].(float64) // JWT expiry is usually a float64 timestamp
	if !ok || time.Now().Unix() > int64(exp) {
		return nil, fmt.Errorf("token expired")
	}

	return claims, nil
}

// Helper functions
func prettifyStruct(obj any) string {
	bytes, _ := json.MarshalIndent(obj, "", "  ")
	return string(bytes)
}

// OpenAI service - keeping your existing implementation
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
			Model:    "gpt-4o-mini",
			Messages: messages,
		},
	)
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Message.Content, nil
}

// Pinecone service - keeping your existing implementation
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

// Session management - keeping your existing implementation
func getOrCreateSession(sessionId, userId string) (string, *ChatSession) {
	if sessionId == "" {
		sessionId = fmt.Sprintf("%s-%s", userId, uuid.New().String())
	}

	session, exists := sessions[sessionId]
	if !exists {
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

	session.Messages = append(session.Messages, ChatMessage{
		Role:    role,
		Content: content,
	})

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

// Auth middleware for Clerk
func authMiddleware(clerkAuth *ClerkAuth) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get the Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			c.Abort()
			return
		}

		// Check for Bearer token format
		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header must be Bearer token"})
			c.Abort()
			return
		}

		// Extract the token
		token := strings.TrimPrefix(authHeader, "Bearer ")

		// Verify the token
		claims, err := clerkAuth.VerifyToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token: " + err.Error()})
			c.Abort()
			return
		}

		// Get user ID from claims
		userId, ok := claims["sub"].(string)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in token"})
			c.Abort()
			return
		}

		// Set user ID in context for downstream handlers
		c.Set("userId", userId)
		c.Next()
	}
}

// Handler setup
func setupRoutes(r *gin.Engine, openaiService *OpenAIService, pineconeService *PineconeService, clerkAuth *ClerkAuth) {
	// Public endpoints (if any)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Protected API group
	api := r.Group("/api")
	api.Use(authMiddleware(clerkAuth))

	api.POST("/save", func(c *gin.Context) {
		var req Data
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		// Get authenticated user ID from context
		userId, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User ID not found in request context"})
			return
		}

		// Use authenticated user ID
		req.UserId = userId.(string)

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

	api.POST("/query", func(c *gin.Context) {
		var req QueryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		// Get authenticated user ID from context
		authenticatedUserId, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User ID not found in request context"})
			return
		}

		// Validate that the user ID in the request matches the authenticated user
		if req.UserId != authenticatedUserId.(string) {
			c.JSON(http.StatusForbidden, gin.H{"error": "User ID in request does not match authenticated user"})
			return
		}

		if req.Text == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Missing required parameter: text"})
			return
		}

		// Get or create session
		sessionId, _ := getOrCreateSession(req.SessionId, authenticatedUserId.(string))

		// Get embedding for the query
		embedding, err := openaiService.GetEmbedding(req.Text)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
			return
		}

		// Search for relevant context in Pinecone
		res, err := pineconeService.QueryVectors(c.Request.Context(), authenticatedUserId.(string), embedding)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query database: " + err.Error()})
			return
		}

		// Process the results
		contextText := ""
		if len(res.Matches) > 0 {
			for i, match := range res.Matches[:min(5, len(res.Matches))] { // Take up to 5
				metadata := match.Vector.Metadata.AsMap()
				contextText += fmt.Sprintf("Result %d: %s (Score: %.2f)\n", i+1, metadata["text"].(string), match.Score)
			}
		} else {
			contextText = "No relevant results found."
		}

		// Add user's query to the session
		addMessageToSession(sessionId, "user", req.Text)

		// Prepare messages for OpenAI
		messages := getSessionMessages(sessionId)

		// Add system message with context if available
		// systemPrompt := "You are like a second brain to the user. Answer the user's question as accurately as possible. And only give relevent responses saved by user no extra unnecessary information. But read all top context results and create a message by extracting information from all and creating a message. Keep the response short and to the point."
		// if contextText != "" {
		// 	systemPrompt += " Use the following context to help answer the user's question: " + contextText
		// }
		systemPrompt := "You are a second brain for the user. Answer the question based only on the user's saved data provided in the context below. If the context includes PDF content, treat it as the text extracted from the user's uploaded PDFs. Do not say you can’t access the PDF—use the context provided. Keep the response concise and relevant."
		if contextText != "" {
			systemPrompt += "\nContext from saved data:\n" + contextText
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

	// Reset session endpoint
	api.POST("/reset-session", func(c *gin.Context) {
		var req struct {
			SessionId string `json:"sessionId" binding:"required"`
			UserId    string `json:"userId" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		// Get authenticated user ID from context
		authenticatedUserId, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User ID not found in request context"})
			return
		}

		// Validate that the user ID in the request matches the authenticated user
		if req.UserId != authenticatedUserId.(string) {
			c.JSON(http.StatusForbidden, gin.H{"error": "User ID in request does not match authenticated user"})
			return
		}

		// Create a new session
		newSessionId := fmt.Sprintf("%s-%s", req.UserId, uuid.New().String())
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

	// Get session endpoint
	api.GET("/session/:sessionId", func(c *gin.Context) {
		sessionId := c.Param("sessionId")

		// Get authenticated user ID from context
		authenticatedUserId, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User ID not found in request context"})
			return
		}

		// Verify session belongs to authenticated user
		if !strings.HasPrefix(sessionId, authenticatedUserId.(string)+"-") {
			c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized to access this session"})
			return
		}

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

	// Add this new route inside the setupRoutes function, within the `api` group
	api.POST("/save-tweet", func(c *gin.Context) {
		var req struct {
			TweetURL string `json:"tweetUrl" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		// Get authenticated user ID from context
		userId, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User ID not found in request context"})
			return
		}

		// Extract tweet ID from URL (e.g., https://x.com/username/status/123456789)
		tweetID := ""
		urlParts := strings.Split(req.TweetURL, "/")
		for i, part := range urlParts {
			if part == "status" && i+1 < len(urlParts) {
				tweetID = urlParts[i+1]
				break
			}
		}
		if tweetID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid tweet URL format"})
			return
		}

		// Fetch tweet from X API
		xApiToken := os.Getenv("X_API_BEARER_TOKEN")
		if xApiToken == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "X API bearer token not configured"})
			return
		}

		client := &http.Client{}
		apiReq, err := http.NewRequest("GET", fmt.Sprintf("https://api.x.com/2/tweets/%s", tweetID), nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request: " + err.Error()})
			return
		}
		apiReq.Header.Set("Authorization", "Bearer "+xApiToken)

		resp, err := client.Do(apiReq)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tweet: " + err.Error()})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("X API returned status: %d", resp.StatusCode)})
			return
		}

		var tweetData struct {
			Data struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tweetData); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse tweet data: " + err.Error()})
			return
		}

		tweetText := tweetData.Data.Text
		if tweetText == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No text found in tweet"})
			return
		}

		// Create Data struct for saving
		data := Data{
			Selected_type: "tweet",
			Text:          tweetText,
			UserId:        userId.(string),
		}

		// Get embedding for the tweet text
		embedding, err := openaiService.GetEmbedding(tweetText)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
			return
		}

		// Generate unique vector ID
		vectorId := fmt.Sprintf("%s-tweet-%d", userId.(string), time.Now().UnixNano())

		// Save to Pinecone
		err = pineconeService.UpsertVector(c.Request.Context(), vectorId, embedding, data)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upsert to database: " + err.Error()})
			return
		}

		// Return success response
		c.JSON(http.StatusOK, UpsertResponse{
			Message:   "Tweet saved successfully",
			Text:      tweetText,
			UserId:    userId.(string),
			Type:      "tweet",
			VectorId:  vectorId,
			Timestamp: time.Now(),
		})
	})

	// Add this inside setupRoutes, within the `api` group
	api.POST("/save-pdf", func(c *gin.Context) {
		// Get authenticated user ID from context (via Clerk middleware)
		userId, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User ID not found in request context"})
			return
		}

		// Retrieve the uploaded PDF file from the form-data
		file, err := c.FormFile("pdf")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to retrieve PDF file: " + err.Error()})
			return
		}

		// Open the uploaded file
		pdfFile, err := file.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open PDF file: " + err.Error()})
			return
		}
		defer pdfFile.Close()

		// Initialize PDF reader using ledongthuc/pdf
		pdfReader, err := pdf.NewReader(pdfFile, file.Size)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize PDF reader: " + err.Error()})
			return
		}

		// Extract text from all pages
		var textBuilder strings.Builder
		numPages := pdfReader.NumPage()
		for i := 1; i <= numPages; i++ {
			page := pdfReader.Page(i)
			if page.V.IsNull() {
				continue // Skip empty or invalid pages
			}
			pageText, err := page.GetPlainText(nil)
			if err != nil {
				continue // Skip pages with extraction errors
			}
			textBuilder.WriteString(pageText + "\n")
		}

		fullText := textBuilder.String()
		if fullText == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No readable text found in PDF"})
			return
		}

		// Chunk the text (500 characters per chunk)
		const chunkSize = 500
		var chunks []string
		for i := 0; i < len(fullText); i += chunkSize {
			end := i + chunkSize
			if end > len(fullText) {
				end = len(fullText)
			}
			chunks = append(chunks, fullText[i:end])
		}

		// Process and store each chunk
		var vectorIds []string
		for chunkIdx, chunk := range chunks {
			// Generate embedding for the chunk
			embedding, err := openaiService.GetEmbedding(chunk)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to generate embedding for chunk %d: %v", chunkIdx, err)})
				return
			}

			// Create a unique vector ID
			vectorId := fmt.Sprintf("%s-pdf-%d-%d", userId.(string), time.Now().UnixNano(), chunkIdx)
			vectorIds = append(vectorIds, vectorId)

			// Prepare data for storage
			data := Data{
				Selected_type: "pdf",
				Text:          chunk,
				UserId:        userId.(string),
			}

			// Upsert the vector into Pinecone
			err = pineconeService.UpsertVector(c.Request.Context(), vectorId, embedding, data)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to store chunk %d in Pinecone: %v", chunkIdx, err)})
				return
			}
		}

		// Return success response
		c.JSON(http.StatusOK, gin.H{
			"message":     "PDF processed and stored successfully",
			"user_id":     userId.(string),
			"type":        "pdf",
			"chunk_count": len(chunks),
			"vector_ids":  vectorIds,
			"timestamp":   time.Now().Format(time.RFC3339),
		})
	})

}

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("Error loading .env file")
	}

	// Check required environment variables
	requiredEnvVars := []string{
		"OPENAI_API_KEY",
		"PINECONE_API_KEY",
		"PINECONE_INDEX_HOST",
		"CLERK_ISSUER_URL",
	}

	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			fmt.Printf("Error: %s environment variable is not set\n", envVar)
			os.Exit(1)
		}
	}

	// Initialize services
	openaiService := NewOpenAIService()

	pineconeService, err := NewPineconeService()
	if err != nil {
		fmt.Printf("Failed to initialize Pinecone service: %v\n", err)
		os.Exit(1)
	}

	clerkAuth, err := NewClerkAuth()
	if err != nil {
		fmt.Printf("Failed to initialize Clerk authentication: %v\n", err)
		os.Exit(1)
	}

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode) // Use release mode in production
	r := gin.Default()

	// Setup CORS if needed for your frontend
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*") // In production, set specific origin
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Setup routes
	setupRoutes(r, openaiService, pineconeService, clerkAuth)

	// Get port from environment or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Server is running on port %s\n", port)
	r.Run(":" + port)
}
