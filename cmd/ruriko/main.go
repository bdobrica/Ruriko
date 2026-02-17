package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/bdobrica/Ruriko/common/crypto"
	"github.com/bdobrica/Ruriko/common/version"
	"github.com/bdobrica/Ruriko/internal/ruriko/app"
	"github.com/bdobrica/Ruriko/internal/ruriko/matrix"
)

func main() {
	fmt.Printf("Ruriko Control Plane\n")
	fmt.Printf("Version: %s\n", version.Version)
	fmt.Printf("Commit: %s\n", version.GitCommit)
	fmt.Printf("Build Time: %s\n", version.BuildTime)
	fmt.Println()

	// Load configuration from environment
	config := loadConfig()

	// Validate required configuration
	if config.Matrix.Homeserver == "" {
		fmt.Fprintf(os.Stderr, "Error: MATRIX_HOMESERVER is required\n")
		os.Exit(1)
	}
	if config.Matrix.UserID == "" {
		fmt.Fprintf(os.Stderr, "Error: MATRIX_USER_ID is required\n")
		os.Exit(1)
	}
	if config.Matrix.AccessToken == "" {
		fmt.Fprintf(os.Stderr, "Error: MATRIX_ACCESS_TOKEN is required\n")
		os.Exit(1)
	}
	if len(config.Matrix.AdminRooms) == 0 {
		fmt.Fprintf(os.Stderr, "Error: MATRIX_ADMIN_ROOMS is required\n")
		os.Exit(1)
	}

	// Load master encryption key
	masterKey, err := crypto.LoadMasterKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\nGenerate a key with: openssl rand -hex 32\n", err)
		os.Exit(1)
	}
	config.MasterKey = masterKey

	// Create application
	ruriko, err := app.New(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize Ruriko: %v\n", err)
		os.Exit(1)
	}
	defer ruriko.Stop()

	// Run application
	if err := ruriko.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running Ruriko: %v\n", err)
		os.Exit(1)
	}
}

// loadConfig loads configuration from environment variables
func loadConfig() *app.Config {
	homeserver := getEnv("MATRIX_HOMESERVER", "")
	userID := getEnv("MATRIX_USER_ID", "")
	accessToken := getEnv("MATRIX_ACCESS_TOKEN", "")
	adminRoomsStr := getEnv("MATRIX_ADMIN_ROOMS", "")
	dbPath := getEnv("DATABASE_PATH", "./ruriko.db")

	var adminRooms []string
	if adminRoomsStr != "" {
		adminRooms = strings.Split(adminRoomsStr, ",")
		// Trim whitespace
		for i := range adminRooms {
			adminRooms[i] = strings.TrimSpace(adminRooms[i])
		}
	}

	return &app.Config{
		DatabasePath: dbPath,
		Matrix: matrix.Config{
			Homeserver:  homeserver,
			UserID:      userID,
			AccessToken: accessToken,
			AdminRooms:  adminRooms,
		},
	}
}

// getEnv gets an environment variable with a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
