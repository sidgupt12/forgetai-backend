package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/pinecone-io/go-pinecone/v3/pinecone"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/protobuf/types/known/structpb"
)

func prettifyStruct(obj any) string {
	bytes, _ := json.MarshalIndent(obj, "", "  ")
	return string(bytes)
}

type Data struct {
	Selected_type string `json:"selected_type"`
	Text          string `json:"text"`
	UserId        string `json:"user_id"`
}

type QueryRequest struct {
	Text   string `json:"text" binding:"required"`
	UserId string `json:"userId" binding:"required"`
}

type QueryResult struct {
	VectorId  string    `json:"vector_id"`
	Score     float32   `json:"score"`
	Text      string    `json:"text"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
}

type QueryResponse struct {
	Message string        `json:"message"`
	Results []QueryResult `json:"results"`
}

type UpsertResponse struct {
	Message   string    `json:"message"`
	Text      string    `json:"text"`
	UserId    string    `json:"user_id"`
	Type      string    `json:"type"`
	VectorId  string    `json:"vector_id"`
	Timestamp time.Time `json:"timestamp"`
}

func main() {

	if err := godotenv.Load(); err != nil {
		fmt.Println("Error loading .env file")
	}

	// Initialize Pinecone client
	ctx := context.Background()

	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey: os.Getenv("PINECONE_API_KEY"),
	})
	if err != nil {
		fmt.Printf("failed to create Pinecone client: %v\n", err)
		return
	}

	r := gin.Default()

	r.POST("/api/save", func(c *gin.Context) {
		var req Data
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		embedding, err := getEmbedding(req.Text)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
			return
		}

		vectorId := fmt.Sprintf("%s-%d", req.UserId, time.Now().UnixNano())

		err = upsertToPinecone(ctx, pc, vectorId, embedding, req)
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

		embedding, err := getEmbedding(text)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get embedding: " + err.Error()})
			return
		}

		res, err := queryFromPinecone(ctx, pc, userId, embedding)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to query database: " + err.Error()})
			return
		}
		if len(res.Matches) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"message": "Query successful, no matches found",
				"results": "",
			})
			return
		}

		topMatch := res.Matches[0] // Top result (highest score)
		metadata := topMatch.Vector.Metadata.AsMap()
		topText := metadata["text"].(string)

		c.JSON(http.StatusOK, gin.H{
			"message": "Query successful",
			"results": topText,
		})

		// prettyResponse := prettifyStruct(res)
		// c.JSON(http.StatusOK, gin.H{
		// 	"message": "Query successful",
		// 	"results": prettyResponse,
		//})

	})

	fmt.Println("Server is running on port 8080")
	r.Run(":8080")

}

func getEmbedding(text string) ([]float32, error) {
	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))
	req := openai.EmbeddingRequest{
		Input: []string{text},
		Model: "text-embedding-3-small",
	}
	resp, err := client.CreateEmbeddings(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("no embedding data returned")
	}
	return resp.Data[0].Embedding, nil
}

func upsertToPinecone(ctx context.Context, pc *pinecone.Client, id string, embedding []float32, data Data) error {

	indexHost := os.Getenv("PINECONE_INDEX_HOST")

	idxConnection, err := pc.Index(pinecone.NewIndexConnParams{
		Host: indexHost,
		//Namespace: data.UserId,
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

	// Upsert the vector
	count, err := idxConnection.UpsertVectors(ctx, []*pinecone.Vector{vector})
	if err != nil {
		return fmt.Errorf("failed to upsert vector: %v", err)
	}

	fmt.Printf("Successfully upserted %d vector(s)!\n", count)
	return nil
}

func queryFromPinecone(ctx context.Context, pc *pinecone.Client, userId string, embedding []float32) (*pinecone.QueryVectorsResponse, error) {

	indexHost := os.Getenv("PINECONE_INDEX_HOST")

	idxConnection, err := pc.Index(pinecone.NewIndexConnParams{
		Host: indexHost,
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
		TopK:            3,
		IncludeValues:   false,
		IncludeMetadata: true,
		MetadataFilter:  filter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query vectors: %v", err)
	}

	fmt.Println(prettifyStruct(res))
	return res, nil

}
