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
