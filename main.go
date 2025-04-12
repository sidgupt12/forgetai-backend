package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/siddhantgupta/forgetai-backend/internal/auth"
	"github.com/siddhantgupta/forgetai-backend/internal/config"
	"github.com/siddhantgupta/forgetai-backend/internal/database"
	"github.com/siddhantgupta/forgetai-backend/internal/handlers"
	"github.com/siddhantgupta/forgetai-backend/internal/services"
)

func main() {

	if os.Getenv("CLOUD_RUN") == "true" {
		fmt.Println("Running in Cloud Run, waiting 5 seconds for network initialization...")
		time.Sleep(5 * time.Second)
	}

	// Load environment variables from .env file
	if err := godotenv.Load(); err != nil {
		fmt.Printf("Warning: .env file not found: %v\n", err)
	}

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	// Check for PORT environment variable (for Cloud Run)
	port := os.Getenv("PORT")
	if port != "" {
		fmt.Printf("Using PORT from environment: %s\n", port)
		cfg.Port = port
	}

	// Print MongoDB connection info (for debugging)
	fmt.Printf("Connecting to MongoDB: %s\n", maskPassword(cfg.MongoDBURI))

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

	mongodb, err := database.NewMongoDB(cfg.MongoDBURI)
	if err != nil {
		fmt.Printf("Failed to initialize MongoDB: %v\n", err)
		os.Exit(1)
	}
	defer mongodb.Close(context.Background())

	fmt.Println("Successfully connected to MongoDB!")

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
		mongodb,
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

	// Create a server with graceful shutdown
	srv := &http.Server{
		Addr:    "0.0.0.0:" + cfg.Port, // Change from ":" + cfg.Port to explicitly bind to all interfaces
		Handler: r,
	}

	// Start server in a goroutine
	go func() {
		fmt.Printf("Server is running on port %s\n", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Server error: %v\n", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("Shutting down server...")

	// Allow 10 seconds for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		fmt.Printf("Server forced to shutdown: %v\n", err)
	}
}

// maskPassword masks the password in a connection string for logging
func maskPassword(uri string) string {
	passwordStart := -1
	passwordEnd := -1

	// Find "@" - the password is before it
	atIndex := -1
	for i := 0; i < len(uri); i++ {
		if uri[i] == '@' {
			atIndex = i
			break
		}
	}

	if atIndex > 0 {
		// Find ":" before "@"
		for i := atIndex; i >= 0; i-- {
			if uri[i] == ':' {
				passwordStart = i + 1
				passwordEnd = atIndex
				break
			}
		}
	}

	if passwordStart >= 0 && passwordEnd >= 0 {
		return uri[:passwordStart] + "********" + uri[passwordEnd:]
	}

	return uri
}
