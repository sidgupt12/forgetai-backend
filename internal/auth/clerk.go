package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/siddhantgupta/forgetai-backend/internal/services"
)

// ClerkAuth handles JWT verification with Clerk
type ClerkAuth struct {
	JWKSet     jwk.Set
	IssuerURL  string
	LastUpdate time.Time
	Redis      *services.RedisService
}

// NewClerkAuth creates a new Clerk authenticator
func NewClerkAuth(redisService *services.RedisService, issuerURL string) (*ClerkAuth, error) {
	if issuerURL == "" {
		return nil, fmt.Errorf("clerk issuer URL is not set")
	}

	auth := &ClerkAuth{
		IssuerURL: issuerURL,
		Redis:     redisService,
	}

	// Fetch JWKs on initialization
	if err := auth.RefreshJWKs(); err != nil {
		return nil, err
	}

	return auth, nil
}

// RefreshJWKs fetches the latest JWKs from Clerk
func (c *ClerkAuth) RefreshJWKs() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try to get JWKs from Redis first
	if c.Redis != nil {
		jwksData, err := c.Redis.GetJWKs(ctx)
		if err == nil && len(jwksData) > 0 {
			set, err := jwk.Parse(jwksData)
			if err == nil {
				c.JWKSet = set
				c.LastUpdate = time.Now()
				return nil
			}
		}
	}

	// Fetch from Clerk if not in Redis
	jwksURL := fmt.Sprintf("%s/.well-known/jwks.json", c.IssuerURL)
	set, err := jwk.Fetch(ctx, jwksURL)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKs: %v", err)
	}

	c.JWKSet = set
	c.LastUpdate = time.Now()

	// Store in Redis for future use
	if c.Redis != nil {
		jwksJSON, err := json.Marshal(set)
		if err == nil {
			c.Redis.StoreJWKs(ctx, jwksJSON)
		}
	}

	return nil
}

// VerifyToken verifies a JWT token from Clerk
func (c *ClerkAuth) VerifyToken(tokenString string) (jwt.MapClaims, error) {
	// Check if JWKs need refreshing (every 30 minutes instead of 24 hours)
	if time.Since(c.LastUpdate) > 30*time.Minute {
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
