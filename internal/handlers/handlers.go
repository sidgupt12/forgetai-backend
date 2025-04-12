package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ledongthuc/pdf"
	"github.com/sashabaranov/go-openai"
	"github.com/siddhantgupta/forgetai-backend/internal/database"
	"github.com/siddhantgupta/forgetai-backend/internal/models"
	"github.com/siddhantgupta/forgetai-backend/internal/services"
	"github.com/siddhantgupta/forgetai-backend/internal/utils"
	"go.mongodb.org/mongo-driver/mongo"
)

// Handlers contains all HTTP handlers
type Handlers struct {
	OpenAI    *services.OpenAIService
	Pinecone  *services.PineconeService
	Redis     *services.RedisService
	Session   *services.SessionService
	DB        *database.MongoDB
	AdminKey  string
	XAPIToken string
}

// NewHandlers creates a new Handlers instance
func NewHandlers(
	openAI *services.OpenAIService,
	pinecone *services.PineconeService,
	redis *services.RedisService,
	session *services.SessionService,
	db *database.MongoDB,
	adminKey string,
	xAPIToken string,
) *Handlers {
	return &Handlers{
		OpenAI:    openAI,
		Pinecone:  pinecone,
		Redis:     redis,
		Session:   session,
		DB:        db,
		AdminKey:  adminKey,
		XAPIToken: xAPIToken,
	}
}

// HealthCheck handles health check requests
// HealthCheck handles health check requests
func (h *Handlers) HealthCheck(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	status := "ok"
	mongoStatus := "ok"
	redisStatus := "ok"

	// Check MongoDB
	if err := h.DB.Ping(ctx); err != nil {
		status = "degraded"
		mongoStatus = fmt.Sprintf("error: %v", err)
	}

	// Check Redis
	_, err := h.Redis.Ping(ctx)
	if err != nil {
		status = "degraded"
		redisStatus = fmt.Sprintf("error: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"status": status,
		"services": gin.H{
			"mongodb": mongoStatus,
			"redis":   redisStatus,
		},
	})
}

// SaveData handles saving data requests
func (h *Handlers) SaveData(c *gin.Context) {
	var req models.Data
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

	embedding, err := h.OpenAI.GetEmbedding(req.Text)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
		return
	}

	vectorId := fmt.Sprintf("%s-%d", req.UserId, time.Now().UnixNano())

	err = h.Pinecone.UpsertVector(c.Request.Context(), vectorId, embedding, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upsert to database: " + err.Error()})
		return
	}

	userData := &database.UserData{
		UserID:     req.UserId,
		VectorID:   vectorId,
		DataType:   req.Selected_type,
		DataValue:  req.Text,
		ChunkIndex: 0,
		CreatedAt:  time.Now(),
	}

	_, err = h.DB.CreateUserData(c.Request.Context(), userData)
	if err != nil {
		// Log error but continue since data is in Pinecone
		fmt.Printf("Warning: Failed to save to MongoDB: %v\n", err)
	}

	c.JSON(http.StatusOK, models.UpsertResponse{
		Message:   "Data saved successfully",
		Text:      req.Text,
		UserId:    req.UserId,
		Type:      req.Selected_type,
		VectorId:  vectorId,
		Timestamp: time.Now(),
	})
}

// QueryData handles query requests
func (h *Handlers) QueryData(c *gin.Context) {
	var req models.QueryRequest
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
	sessionId, _ := h.Session.GetOrCreateSession(req.SessionId, authenticatedUserId.(string))

	// Get embedding for the query
	embedding, err := h.OpenAI.GetEmbedding(req.Text)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
		return
	}

	// Search for relevant context in Pinecone
	res, err := h.Pinecone.QueryVectors(c.Request.Context(), authenticatedUserId.(string), embedding)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query database: " + err.Error()})
		return
	}

	// Process the results
	contextText := ""
	if len(res.Matches) > 0 {
		// Use a map to deduplicate similar content
		uniqueResults := make(map[string]float32)

		for _, match := range res.Matches[:utils.Min(10, len(res.Matches))] { // Take up to 10 matches
			metadata := match.Vector.Metadata.AsMap()
			text := metadata["text"].(string)

			// Use the first 50 chars as a key to avoid duplication of very similar content
			key := text
			if len(key) > 50 {
				key = key[:50]
			}

			// Only keep the highest scoring version of similar content
			if existingScore, exists := uniqueResults[key]; !exists || match.Score > existingScore {
				uniqueResults[key] = match.Score
			}
		}

		// Format results
		resultNum := 1
		for text, score := range uniqueResults {
			if resultNum > 5 {
				break // Only use top 5 unique results
			}

			// Get full text if we truncated for deduplication
			fullText := text
			if len(text) == 50 && len(text) < len(fullText) {
				fullText = text + "..." // Add ellipsis if truncated
			}

			contextText += fmt.Sprintf("Result %d: %s (Relevance: %.2f)\n\n", resultNum, fullText, score)
			resultNum++
		}
	} else {
		contextText = "No relevant information found in your saved data."
	}

	// Add user's query to the session
	h.Session.AddMessageToSession(sessionId, "user", req.Text)

	// Prepare messages for OpenAI
	messages := h.Session.GetSessionMessages(sessionId)

	// Add system message with context if available
	systemPrompt := "You are a second brain for the user. Answer the question based only on the user's saved data provided in the context below. If the context includes PDF content, treat it as the text extracted from the user's uploaded PDFs. Do not say you can't access the PDF—use the context provided. Keep the response concise and relevant."
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
	response, err := h.OpenAI.GetChatCompletion(finalMessages)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get AI response: " + err.Error()})
		return
	}

	// Add assistant's response to the session
	h.Session.AddMessageToSession(sessionId, "assistant", response)

	// Get the session to count messages
	session, _ := h.Session.GetSession(sessionId)

	// Return the response
	c.JSON(http.StatusOK, models.QueryResponse{
		Message:      "Query successful",
		Answer:       response,
		ContextText:  contextText,
		SessionId:    sessionId,
		SessionCount: len(session.Messages) / 2, // Count conversation turns
		Timestamp:    time.Now(),
	})
}

// ResetSession handles session reset requests
func (h *Handlers) ResetSession(c *gin.Context) {
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
	newSessionId, _ := h.Session.GetOrCreateSession("", req.UserId)

	c.JSON(http.StatusOK, gin.H{
		"message":   "Session reset successfully",
		"sessionId": newSessionId,
	})
}

// GetSession handles session retrieval requests
func (h *Handlers) GetSession(c *gin.Context) {
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

	session, exists := h.Session.GetSession(sessionId)
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
}

// SaveTweet handles tweet saving requests
func (h *Handlers) SaveTweet(c *gin.Context) {
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
	if h.XAPIToken == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "X API bearer token not configured"})
		return
	}

	client := &http.Client{}
	apiReq, err := http.NewRequest("GET", fmt.Sprintf("https://api.x.com/2/tweets/%s", tweetID), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request: " + err.Error()})
		return
	}
	apiReq.Header.Set("Authorization", "Bearer "+h.XAPIToken)

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
	data := models.Data{
		Selected_type: "tweet",
		Text:          tweetText,
		UserId:        userId.(string),
	}

	// Get embedding for the tweet text
	embedding, err := h.OpenAI.GetEmbedding(tweetText)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
		return
	}

	// Generate unique vector ID
	vectorId := fmt.Sprintf("%s-tweet-%d", userId.(string), time.Now().UnixNano())

	// Save to Pinecone
	err = h.Pinecone.UpsertVector(c.Request.Context(), vectorId, embedding, data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upsert to database: " + err.Error()})
		return
	}

	userData := &database.UserData{
		UserID:     userId.(string),
		VectorID:   vectorId,
		DataType:   "tweet",
		DataValue:  tweetText,
		ChunkIndex: 0,
		CreatedAt:  time.Now(),
	}

	_, err = h.DB.CreateUserData(c.Request.Context(), userData)
	if err != nil {
		// Log error but continue since data is in Pinecone
		fmt.Printf("Warning: Failed to save tweet to MongoDB: %v\n", err)
	}

	// Return success response
	c.JSON(http.StatusOK, models.UpsertResponse{
		Message:   "Tweet saved successfully",
		Text:      tweetText,
		UserId:    userId.(string),
		Type:      "tweet",
		VectorId:  vectorId,
		Timestamp: time.Now(),
	})
}

// SavePDF handles PDF saving requests
func (h *Handlers) SavePDF(c *gin.Context) {
	// Get authenticated user ID from context
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

	// Create parent record for the PDF
	pdfData := &database.UserData{
		UserID:     userId.(string),
		VectorID:   "parent-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		DataType:   "pdf",
		DataValue:  file.Filename,
		ChunkIndex: 0,
		CreatedAt:  time.Now(),
	}

	pdfRecord, err := h.DB.CreateUserData(c.Request.Context(), pdfData)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save PDF metadata: " + err.Error()})
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
		embedding, err := h.OpenAI.GetEmbedding(chunk)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to generate embedding for chunk %d: %v", chunkIdx, err)})
			return
		}

		// Create a unique vector ID
		vectorId := fmt.Sprintf("%s-pdf-%d-%d", userId.(string), time.Now().UnixNano(), chunkIdx)
		vectorIds = append(vectorIds, vectorId)

		// Prepare data for storage
		data := models.Data{
			Selected_type: "pdf",
			Text:          chunk,
			UserId:        userId.(string),
		}

		// Upsert the vector into Pinecone
		err = h.Pinecone.UpsertVector(c.Request.Context(), vectorId, embedding, data)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to store chunk %d in Pinecone: %v", chunkIdx, err)})
			return
		}

		// Store chunk in MongoDB
		chunkData := &database.UserData{
			UserID:     userId.(string),
			VectorID:   vectorId,
			DataType:   "pdf-chunk",
			DataValue:  chunk,
			ParentID:   &pdfRecord.ID, // Reference to parent
			ChunkIndex: chunkIdx,
			CreatedAt:  time.Now(),
		}

		_, err = h.DB.CreateUserData(c.Request.Context(), chunkData)
		if err != nil {
			// Log error but continue with other chunks
			fmt.Printf("Error saving chunk %d to MongoDB: %v\n", chunkIdx, err)
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
}

// GetUsage handles usage statistics requests
func (h *Handlers) GetUsage(c *gin.Context) {
	userId, exists := c.Get("userId")
	if !exists {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "User ID not found in request context"})
		return
	}

	ctx := c.Request.Context()

	// Get today's date
	today := time.Now().Format("2006-01-02")

	// Check usage for all endpoints
	endpoints := []string{"save", "query", "reset-session", "save-tweet", "save-pdf"}
	usageStats := make(map[string]int)

	for _, endpoint := range endpoints {
		count, err := h.Redis.GetRateLimitCount(ctx, userId.(string), endpoint)
		if err != nil {
			usageStats[endpoint] = -1 // Error state
		} else {
			usageStats[endpoint] = count
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id":            userId.(string),
		"date":               today,
		"usage":              usageStats,
		"limit_per_endpoint": 10,
	})
}

// ClearCache handles cache clearing requests
func (h *Handlers) ClearCache(c *gin.Context) {
	// Check admin API key
	apiKey := c.GetHeader("X-Admin-API-Key")
	if apiKey != h.AdminKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	ctx := c.Request.Context()
	userId := c.Query("userId")

	if userId == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID required"})
		return
	}

	// Clear all rate limiting keys for the specified user
	cleared, err := h.Redis.ClearRateLimits(ctx, userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to clear cache: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("Successfully cleared rate limit cache for user %s", userId),
		"keys_cleared": cleared,
	})
}

// GetUserData handles retrieving user data
func (h *Handlers) GetUserData(c *gin.Context) {
	// Get authenticated user ID
	userID, exists := c.Get("userId")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	// Optional type filter
	dataType := c.Query("type")

	var items []*database.UserData
	var err error

	if dataType != "" {
		items, err = h.DB.GetUserDataByType(c.Request.Context(), userID.(string), dataType)
	} else {
		items, err = h.DB.GetAllUserData(c.Request.Context(), userID.(string))
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user data: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id": userID,
		"items":   items,
		"count":   len(items),
	})
}

// DeleteData handles deleting user data
func (h *Handlers) DeleteData(c *gin.Context) {
	// Get authenticated user ID
	userID, exists := c.Get("userId")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	// Get item ID from URL
	idStr := c.Param("id")

	// Get the data to check ownership and get vector ID
	userData, err := h.DB.GetUserDataByID(c.Request.Context(), idStr)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{"error": "Item not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch item: " + err.Error()})
		}
		return
	}

	// Check ownership
	if userData.UserID != userID.(string) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized to delete this item"})
		return
	}

	// Handle based on data type
	if userData.DataType == "pdf" {
		// Get PDF chunks
		chunks, err := h.DB.GetPDFChunks(c.Request.Context(), idStr)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get PDF chunks: " + err.Error()})
			return
		}

		// Delete each chunk's vector from Pinecone
		for _, chunk := range chunks {
			err = h.Pinecone.DeleteVector(c.Request.Context(), chunk.VectorID)
			if err != nil {
				// Log error but continue
				fmt.Printf("Warning: Failed to delete vector %s from Pinecone: %v\n", chunk.VectorID, err)
			}
		}

		// Delete PDF and chunks from database
		err = h.DB.DeletePDFWithChunks(c.Request.Context(), idStr, userID.(string))
		if err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "Failed to delete PDF from database: " + err.Error()})
			return
		}
	} else {
		// Regular item (note, tweet)

		// Delete vector from Pinecone
		err = h.Pinecone.DeleteVector(c.Request.Context(), userData.VectorID)
		if err != nil {
			// Log error but continue
			fmt.Printf("Warning: Failed to delete vector %s from Pinecone: %v\n", userData.VectorID, err)
		}

		// Delete from database
		err = h.DB.DeleteUserData(c.Request.Context(), idStr, userID.(string))
		if err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "Failed to delete from database: " + err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Item deleted successfully",
		"id":      idStr,
	})
}
