// Gitai is the AI agent runtime binary.
//
// All configuration is loaded from environment variables. The agent connects to
// a Matrix homeserver, loads its Gosuto configuration, starts supervised MCP
// server processes, and begins processing messages using the specified LLM.
//
// Required environment variables:
//
//	GITAI_AGENT_ID        - unique agent identifier (e.g. "weatherbot")
//	GITAI_DB_PATH         - path to the SQLite database (default: /data/gitai.db)
//	MATRIX_HOMESERVER     - Matrix homeserver URL (e.g. "https://matrix.org")
//	MATRIX_USER_ID        - agent's Matrix ID (e.g. "@weatherbot:matrix.org")
//	MATRIX_ACCESS_TOKEN   - agent's Matrix access token
//
// Optional environment variables:
//
//	GITAI_GOSUTO_FILE     - path to initial gosuto.yaml (if not using ACP push)
//	GITAI_ACP_ADDR        - ACP HTTP server listen address (default ":8765")
//	LLM_PROVIDER          - LLM backend: "openai" (default)
//	LLM_API_KEY           - API key for the LLM provider
//	LLM_BASE_URL          - override LLM API base URL (e.g. for Ollama)
//	LLM_MODEL             - model name (e.g. "gpt-4o")
//	LLM_MAX_TOKENS        - max tokens per response (default: provider default)
//	LOG_LEVEL             - "debug", "info", "warn", "error" (default: "info")
//	LOG_FORMAT            - "text" or "json" (default: "text")
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/bdobrica/Ruriko/common/environment"
	"github.com/bdobrica/Ruriko/internal/gitai/app"
	"github.com/bdobrica/Ruriko/internal/gitai/matrix"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	gitai, err := app.New(cfg)
	if err != nil {
		slog.Error("failed to initialize Gitai", "err", err)
		os.Exit(1)
	}

	if err := gitai.Run(); err != nil {
		slog.Error("Gitai exited with error", "err", err)
		os.Exit(1)
	}
}

// loadConfig loads all configuration from environment variables.
// Returns an error (instead of calling os.Exit) so the caller controls process
// termination and the function remains testable.
func loadConfig() (*app.Config, error) {
	agentID, err := environment.RequiredString("GITAI_AGENT_ID")
	if err != nil {
		return nil, err
	}
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

	return &app.Config{
		AgentID:      agentID,
		DatabasePath: environment.StringOr("GITAI_DB_PATH", "/data/gitai.db"),
		GosutoFile:   environment.StringOr("GITAI_GOSUTO_FILE", ""),
		ACPAddr:      environment.StringOr("GITAI_ACP_ADDR", ":8765"),
		LogLevel:     environment.StringOr("LOG_LEVEL", "info"),
		LogFormat:    environment.StringOr("LOG_FORMAT", "text"),
		Matrix: matrix.Config{
			Homeserver:  homeserver,
			UserID:      userID,
			AccessToken: accessToken,
		},
		LLM: app.LLMConfig{
			Provider:  environment.StringOr("LLM_PROVIDER", "openai"),
			APIKey:    environment.StringOr("LLM_API_KEY", ""),
			BaseURL:   environment.StringOr("LLM_BASE_URL", ""),
			Model:     environment.StringOr("LLM_MODEL", "gpt-4o"),
			MaxTokens: environment.IntOr("LLM_MAX_TOKENS", 0),
		},
	}, nil
}
