package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/siddhantgupta/forgetai-backend/internal/auth"
	"github.com/siddhantgupta/forgetai-backend/internal/services"
)

// SetupRoutes configures all routes for the application
func SetupRoutes(
	r *gin.Engine,
	handlers *Handlers,
	clerkAuth *auth.ClerkAuth,
	redisService *services.RedisService,
) {
	// Public endpoints
	r.GET("/health", handlers.HealthCheck)

	// Protected API group
	api := r.Group("/api")
	api.Use(auth.AuthMiddleware(clerkAuth))
	api.Use(auth.RateLimitMiddleware(redisService))

	// Data routes
	api.POST("/save", handlers.SaveData)
	api.POST("/query", handlers.QueryData)

	// Session routes
	api.POST("/reset-session", handlers.ResetSession)
	api.GET("/session/:sessionId", handlers.GetSession)

	// File import routes
	api.POST("/save-tweet", handlers.SaveTweet)
	api.POST("/save-pdf", handlers.SavePDF)

	// Usage routes
	api.GET("/usage", handlers.GetUsage)

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
