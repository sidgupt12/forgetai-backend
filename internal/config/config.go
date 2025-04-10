package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the application
type Config struct {
	Port              string
	OpenAIAPIKey      string
	PineconeAPIKey    string
	PineconeIndexHost string
	ClerkIssuerURL    string
	RedisURL          string
	XAPIBearerToken   string
	AdminAPIKey       string
	MongoDBURI        string
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	// Load .env file if it exists
	_ = godotenv.Load()

	// Check required environment variables
	requiredEnvVars := []string{
		"OPENAI_API_KEY",
		"PINECONE_API_KEY",
		"PINECONE_INDEX_HOST",
		"CLERK_ISSUER_URL",
		"UPSTASH_REDIS_URL",
	}

	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			return nil, fmt.Errorf("%s environment variable is not set", envVar)
		}
	}

	// Get port from environment or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mongoDBURI := os.Getenv("MONGODB_URI")
	if mongoDBURI == "" {
		return nil, fmt.Errorf("MONGODB_URI environment variable is required")
	}

	return &Config{
		Port:              port,
		OpenAIAPIKey:      os.Getenv("OPENAI_API_KEY"),
		PineconeAPIKey:    os.Getenv("PINECONE_API_KEY"),
		PineconeIndexHost: os.Getenv("PINECONE_INDEX_HOST"),
		ClerkIssuerURL:    os.Getenv("CLERK_ISSUER_URL"),
		RedisURL:          os.Getenv("UPSTASH_REDIS_URL"),
		XAPIBearerToken:   os.Getenv("X_API_BEARER_TOKEN"),
		AdminAPIKey:       os.Getenv("ADMIN_API_KEY"),
		MongoDBURI:        mongoDBURI,
	}, nil
}
