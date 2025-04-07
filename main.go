package main

import (
	"fmt"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/siddhantgupta/forgetai-backend/internal/auth"
	"github.com/siddhantgupta/forgetai-backend/internal/config"
	"github.com/siddhantgupta/forgetai-backend/internal/handlers"
	"github.com/siddhantgupta/forgetai-backend/internal/services"
)

func main() {
	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize services
	openaiService := services.NewOpenAIService(cfg.OpenAIAPIKey)

	pineconeService, err := services.NewPineconeService(cfg.PineconeAPIKey, cfg.PineconeIndexHost)
	if err != nil {
		fmt.Printf("Failed to initialize Pinecone service: %v\n", err)
		os.Exit(1)
	}

	redisService, err := services.NewRedisService(cfg.RedisURL)
	if err != nil {
		fmt.Printf("Failed to initialize Redis service: %v\n", err)
		os.Exit(1)
	}

	sessionService := services.NewSessionService()

	clerkAuth, err := auth.NewClerkAuth(redisService, cfg.ClerkIssuerURL)
	if err != nil {
		fmt.Printf("Failed to initialize Clerk authentication: %v\n", err)
		os.Exit(1)
	}

	// Initialize handlers
	apiHandlers := handlers.NewHandlers(
		openaiService,
		pineconeService,
		redisService,
		sessionService,
		cfg.AdminAPIKey,
		cfg.XAPIBearerToken,
	)

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode) // Use release mode in production
	r := gin.Default()

	// Setup CORS
	r.Use(handlers.SetupCORS())

	// Setup routes
	handlers.SetupRoutes(r, apiHandlers, clerkAuth, redisService)

	// Start server
	fmt.Printf("Server is running on port %s\n", cfg.Port)
	r.Run(":" + cfg.Port)
}
