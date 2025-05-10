package services

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

// RedisService handles Redis connections and operations
type RedisService struct {
	client *redis.Client
}

// NewRedisService creates a new Redis service
func NewRedisService(redisURL string) (*RedisService, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %v", err)
	}

	client := redis.NewClient(opt)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
	}

	return &RedisService{
		client: client,
	}, nil
}

// CheckRateLimit checks if a user has exceeded their API call limit
// Returns true if rate limit is exceeded, false otherwise
func (s *RedisService) CheckRateLimit(ctx context.Context, userId, endpoint string) (bool, error) {
	key := fmt.Sprintf("rate-limit:%s:%s:%s", userId, endpoint, time.Now().Format("2006-01-02"))

	// Increment the counter
	count, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check rate limit: %v", err)
	}

	// Set expiry if this is a new key (30 minutes instead of 24 hours)
	if count == 1 {
		err = s.client.Expire(ctx, key, 30*time.Minute).Err()
		if err != nil {
			return false, fmt.Errorf("failed to set expiry on rate limit key: %v", err)
		}
	}

	// Check if rate limit exceeded (10 calls per user per endpoint per day)
	return count > 30, nil
}

// GetRateLimitCount returns the current rate limit count for a user and endpoint
func (s *RedisService) GetRateLimitCount(ctx context.Context, userId, endpoint string) (int, error) {
	key := fmt.Sprintf("rate-limit:%s:%s:%s", userId, endpoint, time.Now().Format("2006-01-02"))

	count, err := s.client.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil // Key doesn't exist, so count is 0
	} else if err != nil {
		return 0, fmt.Errorf("failed to get rate limit count: %v", err)
	}

	return count, nil
}

// StoreJWKs stores JWKS in Redis cache
func (s *RedisService) StoreJWKs(ctx context.Context, jwksData []byte) error {
	return s.client.Set(ctx, "clerk-jwks", jwksData, 30*time.Minute).Err()
	// return s.client.Set(ctx, "clerk-jwks", jwksData, 24*time.hours).Err()
}

// GetJWKs retrieves JWKS from Redis cache
func (s *RedisService) GetJWKs(ctx context.Context) ([]byte, error) {
	return s.client.Get(ctx, "clerk-jwks").Bytes()
}

// ClearRateLimits clears all rate limiting keys for a specific user
func (s *RedisService) ClearRateLimits(ctx context.Context, userId string) (int64, error) {
	pattern := fmt.Sprintf("rate-limit:%s:*", userId)
	keys, err := s.client.Keys(ctx, pattern).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to find keys: %v", err)
	}

	if len(keys) == 0 {
		return 0, nil
	}

	return s.client.Del(ctx, keys...).Result()
}

// Ping checks if the Redis connection is alive
func (s *RedisService) Ping(ctx context.Context) (string, error) {
	return s.client.Ping(ctx).Result()
}
