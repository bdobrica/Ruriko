package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/bdobrica/Ruriko/common/crypto"
	"github.com/bdobrica/Ruriko/common/environment"
	"github.com/bdobrica/Ruriko/common/version"
	"github.com/bdobrica/Ruriko/internal/ruriko/app"
	"github.com/bdobrica/Ruriko/internal/ruriko/matrix"
	"github.com/bdobrica/Ruriko/internal/ruriko/provisioning"
)

func main() {
	// Configure structured logging as early as possible so all subsequent
	// messages use the correct handler and level.
	configureLogging()

	fmt.Printf("Ruriko Control Plane\n")
	fmt.Printf("Version: %s\n", version.Version)
	fmt.Printf("Commit: %s\n", version.GitCommit)
	fmt.Printf("Build Time: %s\n", version.BuildTime)
	fmt.Println()

	// Load configuration from environment — returns an error instead of
	// calling os.Exit so that the validation message is explicit.
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

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

// loadConfig loads all configuration from environment variables.
// Returns an error (instead of calling os.Exit) so the caller controls process
// termination and the function remains testable.
func loadConfig() (*app.Config, error) {
	homeserver, err := environment.RequiredString("MATRIX_HOMESERVER")
	if err != nil {
		return nil, err
	}
	userID, err := environment.RequiredString("MATRIX_USER_ID")
	if err != nil {
		return nil, err
	}
	accessToken, err := environment.RequiredString("MATRIX_ACCESS_TOKEN")
	if err != nil {
		return nil, err
	}
	adminRooms := environment.StringSliceOr("MATRIX_ADMIN_ROOMS", nil)
	if len(adminRooms) == 0 {
		return nil, fmt.Errorf("required environment variable %q is not set", "MATRIX_ADMIN_ROOMS")
	}

	// Load and decode the master encryption key.
	masterKeyHex, err := environment.RequiredString("RURIKO_MASTER_KEY")
	if err != nil {
		return nil, fmt.Errorf("%w\nGenerate a key with: openssl rand -hex 32", err)
	}
	masterKey, err := crypto.ParseMasterKey(masterKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid RURIKO_MASTER_KEY: %w", err)
	}

	adminSenders := environment.StringSliceOr("MATRIX_ADMIN_SENDERS", nil)
	dbPath := environment.StringOr("DATABASE_PATH", "./ruriko.db")
	enableDocker := environment.BoolOr("DOCKER_ENABLE", false)
	dockerNetwork := environment.StringOr("DOCKER_NETWORK", "")
	reconcileInterval := environment.DurationOr("RECONCILE_INTERVAL", 30*1e9) // 30s

	// Optional Matrix provisioning configuration.
	// Only enabled when MATRIX_PROVISIONING_ENABLE=true.
	var provisioningCfg *provisioning.Config
	if environment.BoolOr("MATRIX_PROVISIONING_ENABLE", false) {
		hsType := provisioning.HomeserverType(environment.StringOr("MATRIX_HOMESERVER_TYPE", string(provisioning.HomeserverTuwunel)))
		provisioningCfg = &provisioning.Config{
			Homeserver:        homeserver,
			AdminUserID:       userID,
			AdminAccessToken:  accessToken,
			HomeserverType:    hsType,
			SharedSecret:      environment.StringOr("MATRIX_SHARED_SECRET", ""),
			RegistrationToken: environment.StringOr("TUWUNEL_REGISTRATION_TOKEN", ""),
			UsernameSuffix:    environment.StringOr("MATRIX_AGENT_USERNAME_SUFFIX", ""),
			AdminRooms:        adminRooms,
		}
	}

	return &app.Config{
		MasterKey:         masterKey,
		DatabasePath:      dbPath,
		EnableDocker:      enableDocker,
		DockerNetwork:     dockerNetwork,
		ReconcileInterval: reconcileInterval,
		AdminSenders:      adminSenders,
		Provisioning:      provisioningCfg,
		HTTPAddr:          environment.StringOr("HTTP_ADDR", ""),
		KuzeBaseURL:       environment.StringOr("KUZE_BASE_URL", ""),
		KuzeTTL:           environment.DurationOr("KUZE_TTL", 0),
		DefaultAgentImage: environment.StringOr("DEFAULT_AGENT_IMAGE", ""),
		AuditRoomID:       environment.StringOr("MATRIX_AUDIT_ROOM", ""),
		TemplatesFS:       loadTemplatesFS(),
		Matrix: matrix.Config{
			Homeserver:  homeserver,
			UserID:      userID,
			AccessToken: accessToken,
			AdminRooms:  adminRooms,
		},
		// --- R9: Natural Language Interface ---
		// NLPProvider is left nil so that app.New auto-constructs one from
		// the env vars below (or stays in keyword-matching mode).
		NLPModel:           environment.StringOr("NLP_MODEL", ""),
		NLPEndpoint:        environment.StringOr("NLP_ENDPOINT", ""),
		NLPAPIKeySecretRef: environment.StringOr("NLP_API_KEY_ENV", ""),
		NLPRateLimit:       environment.IntOr("NLP_RATE_LIMIT", 0),
		NLPTokenBudget:     environment.IntOr("NLP_TOKEN_BUDGET", 0),
	}, nil
}

// loadTemplatesFS returns a fs.FS for the Gosuto templates directory.
// The directory is determined by the TEMPLATES_DIR env var (default: ./templates).
// Returns nil if the directory does not exist (templates will be unavailable).
func loadTemplatesFS() fs.FS {
	dir := environment.StringOr("TEMPLATES_DIR", "./templates")
	if _, err := os.Stat(dir); err != nil {
		slog.Warn("templates directory not found; gosuto templates unavailable", "dir", dir)
		return nil
	}
	slog.Info("templates directory found", "dir", dir)
	return os.DirFS(dir)
}

// configureLogging initialises the global slog logger from environment variables.
//
// Supported variables:
//   - LOG_LEVEL:  debug | info | warn | error  (default: info)
//   - LOG_FORMAT: text | json                  (default: text)
func configureLogging() {
	levelStr := environment.StringOr("LOG_LEVEL", "info")
	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if environment.StringOr("LOG_FORMAT", "text") == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
	slog.Debug("logging configured", "level", level.String())
}
