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
	"strconv"

	"github.com/bdobrica/Ruriko/internal/gitai/app"
	"github.com/bdobrica/Ruriko/internal/gitai/matrix"
)

func main() {
	cfg := &app.Config{
		AgentID:      requireEnv("GITAI_AGENT_ID"),
		DatabasePath: envOr("GITAI_DB_PATH", "/data/gitai.db"),
		GosutoFile:   os.Getenv("GITAI_GOSUTO_FILE"),
		ACPAddr:      envOr("GITAI_ACP_ADDR", ":8765"),
		LogLevel:     envOr("LOG_LEVEL", "info"),
		LogFormat:    envOr("LOG_FORMAT", "text"),
		Matrix: matrix.Config{
			Homeserver:  requireEnv("MATRIX_HOMESERVER"),
			UserID:      requireEnv("MATRIX_USER_ID"),
			AccessToken: requireEnv("MATRIX_ACCESS_TOKEN"),
		},
		LLM: app.LLMConfig{
			Provider:  envOr("LLM_PROVIDER", "openai"),
			APIKey:    os.Getenv("LLM_API_KEY"),
			BaseURL:   os.Getenv("LLM_BASE_URL"),
			Model:     envOr("LLM_MODEL", "gpt-4o"),
			MaxTokens: envInt("LLM_MAX_TOKENS", 0),
		},
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

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "fatal: required environment variable %q is not set\n", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
