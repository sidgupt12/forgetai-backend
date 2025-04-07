package services

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
	"github.com/siddhantgupta/forgetai-backend/internal/models"
)

// SessionService manages chat sessions
type SessionService struct {
	sessions map[string]models.ChatSession
	mu       sync.RWMutex // For thread-safe access
}

// NewSessionService creates a new session service
func NewSessionService() *SessionService {
	return &SessionService{
		sessions: make(map[string]models.ChatSession),
	}
}

// GetOrCreateSession gets an existing session or creates a new one
func (s *SessionService) GetOrCreateSession(sessionId, userId string) (string, *models.ChatSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sessionId == "" {
		sessionId = fmt.Sprintf("%s-%s", userId, uuid.New().String())
	}

	session, exists := s.sessions[sessionId]
	if !exists {
		session = models.ChatSession{
			Messages:  []models.ChatMessage{},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		s.sessions[sessionId] = session
	}

	return sessionId, &session
}

// AddMessageToSession adds a message to a session
func (s *SessionService) AddMessageToSession(sessionId string, role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[sessionId]
	if !exists {
		return
	}

	session.Messages = append(session.Messages, models.ChatMessage{
		Role:    role,
		Content: content,
	})

	if len(session.Messages) > 10 {
		session.Messages = session.Messages[len(session.Messages)-10:]
	}

	session.UpdatedAt = time.Now()
	s.sessions[sessionId] = session
}

// GetSessionMessages gets messages from a session in OpenAI format
func (s *SessionService) GetSessionMessages(sessionId string) []openai.ChatCompletionMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, exists := s.sessions[sessionId]
	if !exists {
		return []openai.ChatCompletionMessage{}
	}

	return models.ToOpenAIChatMessages(session.Messages)
}

// GetSession gets a session by ID
func (s *SessionService) GetSession(sessionId string) (models.ChatSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, exists := s.sessions[sessionId]
	return session, exists
}

// GetSessionCount returns the number of sessions
func (s *SessionService) GetSessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.sessions)
}
