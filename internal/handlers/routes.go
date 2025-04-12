package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/siddhantgupta/forgetai-backend/internal/auth"
	"github.com/siddhantgupta/forgetai-backend/internal/services"
)

func SetupRoutes(
	r *gin.Engine,
	handlers *Handlers,
	clerkAuth *auth.ClerkAuth,
	redisService *services.RedisService,
) {

	r.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "OK")
	})

	// Public endpoints
	r.GET("/health", handlers.HealthCheck)

	// Protected API group - all endpoints require authentication
	api := r.Group("/api")
	api.Use(auth.AuthMiddleware(clerkAuth))

	// Non-rate-limited endpoints (data retrieval and session management)
	api.GET("/data", handlers.GetUserData)              // MongoDB data retrieval
	api.DELETE("/data/:id", handlers.DeleteData)        // MongoDB data deletion
	api.GET("/session/:sessionId", handlers.GetSession) // Get session
	api.GET("/usage", handlers.GetUsage)                // Usage statistics

	// Rate-limited endpoints (resource-intensive operations)
	rateLimited := api.Group("/")
	rateLimited.Use(auth.RateLimitMiddleware(redisService))

	// Data creation routes (rate-limited)
	rateLimited.POST("/save", handlers.SaveData)
	rateLimited.POST("/query", handlers.QueryData)
	rateLimited.POST("/reset-session", handlers.ResetSession)
	rateLimited.POST("/save-tweet", handlers.SaveTweet)
	rateLimited.POST("/save-pdf", handlers.SavePDF)

	// Admin routes
	r.POST("/admin/clear-cache", handlers.ClearCache)
}

// SetupCORS configures CORS for the application
func SetupCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*") // In production, set specific origin
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, X-Admin-API-Key, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
