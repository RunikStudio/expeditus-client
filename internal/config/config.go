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

// LoadLoginConfig loads the login configuration from environment variables.
// It looks for a .env file in the project root and falls back to actual environment variables.
func LoadLoginConfig() (*LoginConfig, error) {
	loadEnvFile()

	cfg := &LoginConfig{
		TargetURL: getEnvOrDefault("DELFOS_URL", "https://www.delfos.tur.ar/"),
		Username:  os.Getenv("DELFOS_USER"),
		Password:  os.Getenv("DELFOS_PASSWORD"),
	}

	if cfg.Username == "" {
		return nil, fmt.Errorf("DELFOS_USER environment variable is required")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("DELFOS_PASSWORD environment variable is required")
	}

	return cfg, nil
}

// loadEnvFile attempts to load the .env file from the project root.
// It searches relative to this file's location to find the project root.
func loadEnvFile() {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return
	}

	// Navigate from internal/config/config.go to project root
	projectRoot := filepath.Join(filepath.Dir(currentFile), "..", "..")
	envPath := filepath.Join(projectRoot, ".env")

	// Ignore errors - if .env doesn't exist, we'll use actual environment variables
	_ = godotenv.Load(envPath)
}

// getEnvOrDefault returns the environment variable value or a default if not set
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
