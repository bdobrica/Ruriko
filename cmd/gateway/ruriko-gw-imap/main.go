// ruriko-gw-imap is a placeholder IMAP event gateway for Gitai agents.
//
// # Overview
//
// This binary demonstrates the external gateway binary contract: it polls an
// IMAP mailbox for new messages and forwards each message as a normalised event
// envelope to the agent's local ACP endpoint (POST /events/{source}).
//
// # PLACEHOLDER STATUS
//
// The IMAP polling loop is intentionally stubbed out. The binary compiles,
// starts, validates its configuration, and then waits without connecting to any
// mail server. This is sufficient to:
//
//   - Establish the binary artefact that proves the Docker image build layer works.
//   - Validate the ACP integration path (POST /events/{source}).
//   - Document the full configuration contract for a production implementation.
//
// A production implementation would use an IMAP client library (e.g.
// github.com/emersion/go-imap) to authenticate, watch for new messages with
// IMAP IDLE, and translate each message into the event envelope below.
//
// # Configuration (environment variables)
//
//	ACP_URL          Base URL of the agent's ACP server, e.g. http://localhost:8765 (required)
//	ACP_TOKEN        Bearer token for ACP authentication (optional)
//	GW_SOURCE        Gateway source name matching the Gosuto config entry, e.g. "imap" (required)
//	GW_IMAP_HOST     IMAP server hostname, e.g. "imap.example.com" (required)
//	GW_IMAP_PORT     IMAP server port (default: "993" for TLS)
//	GW_IMAP_USER     IMAP account username (required)
//	GW_IMAP_PASSWORD IMAP account password (required)
//	GW_IMAP_MAILBOX  Mailbox/folder to watch (default: "INBOX")
//	GW_POLL_INTERVAL Poll interval for servers that don't support IMAP IDLE (default: "60s")
//	LOG_FORMAT       "text" or "json" (default: "text")
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// ─── Config ──────────────────────────────────────────────────────────────────

type config struct {
	ACPURL       string
	ACPToken     string
	Source       string
	IMAPHost     string
	IMAPPort     string
	IMAPUser     string
	IMAPPassword string
	IMAPMailbox  string
	PollInterval time.Duration
}

func loadConfig() (*config, error) {
	cfg := &config{
		ACPURL:       os.Getenv("ACP_URL"),
		ACPToken:     os.Getenv("ACP_TOKEN"),
		Source:       os.Getenv("GW_SOURCE"),
		IMAPHost:     os.Getenv("GW_IMAP_HOST"),
		IMAPPort:     os.Getenv("GW_IMAP_PORT"),
		IMAPUser:     os.Getenv("GW_IMAP_USER"),
		IMAPPassword: os.Getenv("GW_IMAP_PASSWORD"),
		IMAPMailbox:  os.Getenv("GW_IMAP_MAILBOX"),
	}

	for _, req := range []struct{ name, val string }{
		{"ACP_URL", cfg.ACPURL},
		{"GW_SOURCE", cfg.Source},
		{"GW_IMAP_HOST", cfg.IMAPHost},
		{"GW_IMAP_USER", cfg.IMAPUser},
		{"GW_IMAP_PASSWORD", cfg.IMAPPassword},
	} {
		if req.val == "" {
			return nil, fmt.Errorf("required environment variable %s is not set", req.name)
		}
	}

	if cfg.IMAPPort == "" {
		cfg.IMAPPort = "993"
	}
	if cfg.IMAPMailbox == "" {
		cfg.IMAPMailbox = "INBOX"
	}

	pollStr := os.Getenv("GW_POLL_INTERVAL")
	if pollStr == "" {
		pollStr = "60s"
	}
	d, err := time.ParseDuration(pollStr)
	if err != nil {
		return nil, fmt.Errorf("invalid GW_POLL_INTERVAL %q: %w", pollStr, err)
	}
	cfg.PollInterval = d

	return cfg, nil
}

// ─── ACP event types ──────────────────────────────────────────────────────────

// acpEvent is the normalised envelope posted to ACP POST /events/{source}.
// This mirrors common/spec/envelope.Event — reproduced here so the binary has
// zero in-tree dependencies and can be built as a standalone artefact.
type acpEvent struct {
	Source  string          `json:"source"`
	Type    string          `json:"type"`
	TS      time.Time       `json:"ts"`
	Payload acpEventPayload `json:"payload"`
}

type acpEventPayload struct {
	Message string                 `json:"message"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// postEvent sends a single event envelope to the agent's ACP endpoint.
func postEvent(ctx context.Context, cfg *config, evt acpEvent) error {
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	url := cfg.ACPURL + "/events/" + cfg.Source
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.ACPToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.ACPToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ACP returned HTTP %d for %s", resp.StatusCode, url)
	}

	return nil
}

// ─── Gateway loop (placeholder) ───────────────────────────────────────────────

// runGateway is the main polling loop. In a production implementation this
// would authenticate to the IMAP server, watch for new messages using IMAP
// IDLE (RFC 2177), and call postEvent for each new message. The placeholder
// loop simply sleeps and logs to demonstrate the lifecycle without requiring
// a live mail server.
//
// The function returns when ctx is cancelled (e.g. on SIGTERM).
func runGateway(ctx context.Context, cfg *config) {
	slog.Info("ruriko-gw-imap started",
		"source", cfg.Source,
		"imap_host", cfg.IMAPHost,
		"imap_port", cfg.IMAPPort,
		"imap_user", cfg.IMAPUser,
		"mailbox", cfg.IMAPMailbox,
		"poll_interval", cfg.PollInterval,
		"placeholder", true,
	)
	slog.Warn("PLACEHOLDER: IMAP polling is not implemented; binary exists to validate Docker image build layer")

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("ruriko-gw-imap shutting down")
			return
		case t := <-ticker.C:
			slog.Debug("poll tick (placeholder — no IMAP connection)",
				"source", cfg.Source,
				"tick", t.UTC().Format(time.RFC3339),
			)
			// Production implementation would:
			//   1. Connect to cfg.IMAPHost:cfg.IMAPPort using TLS
			//   2. Authenticate with cfg.IMAPUser / cfg.IMAPPassword
			//   3. SELECT cfg.IMAPMailbox
			//   4. SEARCH UNSEEN (or use IMAP IDLE for push notification)
			//   5. For each unseen message, build and send:
			//      postEvent(ctx, cfg, acpEvent{
			//          Source: cfg.Source,
			//          Type:   "imap.email",
			//          TS:     time.Now().UTC(),
			//          Payload: acpEventPayload{
			//              Message: "New email from <from>: <subject>",
			//              Data:    map[string]interface{}{"from": from, "subject": subject},
			//          },
			//      })
		}
	}
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	// Configure structured logging.
	logLevel := slog.LevelInfo
	var logHandler slog.Handler
	if os.Getenv("LOG_FORMAT") == "json" {
		logHandler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	} else {
		logHandler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	}
	slog.SetDefault(slog.New(logHandler))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	runGateway(ctx, cfg)
}
