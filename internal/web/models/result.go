package models

import "time"

// Result represents a scraped data item
type Result struct {
	ID         string                 `json:"id"`
	Data       map[string]interface{} `json:"data"`
	Screenshot string                 `json:"screenshot,omitempty"` // Base64 encoded screenshot
	Timestamp  time.Time              `json:"timestamp"`
	Error      string                 `json:"error,omitempty"`
}

// NewResult creates a new result
func NewResult(id string, data map[string]interface{}) Result {
	return Result{
		ID:        id,
		Data:      data,
		Timestamp: time.Now(),
	}
}

// NewResultWithError creates a new result with error
func NewResultWithError(id string, err string) Result {
	return Result{
		ID:        id,
		Data:      make(map[string]interface{}),
		Timestamp: time.Now(),
		Error:     err,
	}
}
