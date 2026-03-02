package models

import (
	"sync"
	"time"
)

// Session status constants
const (
	SessionStatusPending   = "pending"
	SessionStatusRunning   = "running"
	SessionStatusCompleted = "completed"
	SessionStatusFailed    = "failed"
	SessionStatusCancelled = "cancelled"
)

// Session represents a scraping session
type Session struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	Progress  float64    `json:"progress"`
	StartTime time.Time  `json:"startTime"`
	EndTime   *time.Time `json:"endTime,omitempty"`
	Error     *string    `json:"error,omitempty"`
	Results   []Result   `json:"results,omitempty"`
	mu        sync.RWMutex
}

// NewSession creates a new session
func NewSession(id string) *Session {
	return &Session{
		ID:        id,
		Status:    SessionStatusPending,
		Progress:  0,
		StartTime: time.Now(),
		Results:   make([]Result, 0),
	}
}

// SetStatus sets the session status
func (s *Session) SetStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
}

// SetProgress sets the session progress
func (s *Session) SetProgress(progress float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Progress = progress
}

// SetError sets the session error
func (s *Session) SetError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Error = &err
}

// Complete marks the session as completed
func (s *Session) Complete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = SessionStatusCompleted
	now := time.Now()
	s.EndTime = &now
}

// Fail marks the session as failed
func (s *Session) Fail(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = SessionStatusFailed
	s.Error = &err
	now := time.Now()
	s.EndTime = &now
}

// Cancel marks the session as cancelled
func (s *Session) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = SessionStatusCancelled
	now := time.Now()
	s.EndTime = &now
}

// AddResult adds a result to the session
func (s *Session) AddResult(result Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Results = append(s.Results, result)
}

// GetResults returns a copy of results
func (s *Session) GetResults() []Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	results := make([]Result, len(s.Results))
	copy(results, s.Results)
	return results
}

// GetStatus returns a copy of status
func (s *Session) GetStatus() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

// GetProgress returns a copy of progress
func (s *Session) GetProgress() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Progress
}
