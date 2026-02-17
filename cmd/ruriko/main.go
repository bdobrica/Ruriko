package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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
	adminSendersStr := getEnv("MATRIX_ADMIN_SENDERS", "")
	dbPath := getEnv("DATABASE_PATH", "./ruriko.db")

	var adminRooms []string
	if adminRoomsStr != "" {
		adminRooms = strings.Split(adminRoomsStr, ",")
		// Trim whitespace
		for i := range adminRooms {
			adminRooms[i] = strings.TrimSpace(adminRooms[i])
		}
	}

	var adminSenders []string
	if adminSendersStr != "" {
		adminSenders = strings.Split(adminSendersStr, ",")
		for i := range adminSenders {
			adminSenders[i] = strings.TrimSpace(adminSenders[i])
		}
	}

	enableDocker := getEnvBool("DOCKER_ENABLE", false)
	dockerNetwork := getEnv("DOCKER_NETWORK", "")
	reconcileIntervalStr := getEnv("RECONCILE_INTERVAL", "30s")
	reconcileInterval, err := time.ParseDuration(reconcileIntervalStr)
	if err != nil {
		reconcileInterval = 30 * time.Second
	}

	return &app.Config{
		DatabasePath:      dbPath,
		EnableDocker:      enableDocker,
		DockerNetwork:     dockerNetwork,
		ReconcileInterval: reconcileInterval,
		AdminSenders:      adminSenders,
		Matrix: matrix.Config{
			Homeserver:  homeserver,
			UserID:      userID,
			AccessToken: accessToken,
			AdminRooms:  adminRooms,
		},
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}
