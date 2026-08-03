package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/joho/godotenv"
)

// LoginConfig holds the authentication credentials
type LoginConfig struct {
	TargetURL string
	Username  string
	Password  string
}

// APIConfig holds the Expeditus API configuration
type APIConfig struct {
	BaseURL   string
	AccountID string
}

// LoadLoginConfig loads the login configuration from environment variables.
// Falls back to hardcoded defaults if not provided.
func LoadLoginConfig() (*LoginConfig, error) {
	loadEnvFile()

	cfg := &LoginConfig{
		TargetURL: getEnvOrDefault("DELFOS_URL", "https://www.delfos.tur.ar/"),
		Username:  getEnvOrDefault("DELFOS_USER", "jpardo"),
		Password:  getEnvOrDefault("DELFOS_PASSWORD", "Grupomas2025*"),
	}

	if cfg.Username == "" {
		return nil, fmt.Errorf("DELFOS_USER environment variable is required")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("DELFOS_PASSWORD environment variable is required")
	}

	return cfg, nil
}

// LoadAPIConfig loads the API configuration from environment variables.
func LoadAPIConfig() (*APIConfig, error) {
	loadEnvFile()

	cfg := &APIConfig{
		BaseURL:   getEnvOrDefault("EXPEDITUS_API_URL", "http://localhost:8083"),
		AccountID: os.Getenv("EXPEDITUS_ACCOUNT_ID"), // Optional - if empty, gets all accounts
	}

	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("EXPEDITUS_API_URL environment variable is required")
	}

	return cfg, nil
}

// loadEnvFile attempts to load the .env file.
// It first tries the current working directory (where the binary runs),
// then falls back to the project root for development.
func loadEnvFile() {
	// Try current working directory first (production)
	if err := godotenv.Load(".env"); err == nil {
		return
	}

	// Try project root (development)
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return
	}

	projectRoot := filepath.Join(filepath.Dir(currentFile), "..", "..")
	envPath := filepath.Join(projectRoot, ".env")

	_ = godotenv.Load(envPath)
}

// getEnvOrDefault returns the environment variable value or a default if not set
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
