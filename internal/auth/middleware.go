package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/siddhantgupta/forgetai-backend/internal/services"
)

// AuthMiddleware creates a middleware for Clerk authentication
func AuthMiddleware(clerkAuth *ClerkAuth) gin.HandlerFunc {
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

// RateLimitMiddleware creates a middleware for rate limiting
func RateLimitMiddleware(redisService *services.RedisService) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get user ID from context (set by auth middleware)
		userId, exists := c.Get("userId")
		if !exists {
			c.Next()
			return
		}

		// Extract endpoint from request path
		path := c.Request.URL.Path
		endpoint := strings.TrimPrefix(path, "/api/")
		if idx := strings.Index(endpoint, "/"); idx > 0 {
			endpoint = endpoint[:idx] // Only use the first part of the path
		}

		// Check rate limit
		exceeded, err := redisService.CheckRateLimit(c.Request.Context(), userId.(string), endpoint)
		if err != nil {
			// Log error but let request through if there's an issue with rate limiting
			c.Next()
			return
		}

		if exceeded {
			count, _ := redisService.GetRateLimitCount(c.Request.Context(), userId.(string), endpoint)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "Rate limit exceeded. Maximum 10 requests per API endpoint per day.",
				"limit":       10,
				"count":       count,
				"retry_after": "Try again tomorrow",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
