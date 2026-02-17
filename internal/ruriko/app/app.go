// Package app provides the main Ruriko application
package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/matrix"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// Config holds application configuration
type Config struct {
	DatabasePath string
	MasterKey    []byte
	Matrix       matrix.Config
}

// App is the main Ruriko application
type App struct {
	config   *Config
	store    *store.Store
	secrets  *secrets.Store
	matrix   *matrix.Client
	router   *commands.Router
	handlers *commands.Handlers
}

// New creates a new Ruriko application
func New(config *Config) (*App, error) {
	// Initialize database
	fmt.Printf("Opening database: %s\n", config.DatabasePath)
	store, err := store.New(config.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Initialize Matrix client
	fmt.Printf("Connecting to Matrix homeserver: %s\n", config.Matrix.Homeserver)
	matrixClient, err := matrix.New(&config.Matrix)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to initialize Matrix client: %w", err)
	}

	// Initialize secrets store
	secretsStore, err := secrets.New(store, config.MasterKey)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to initialize secrets store: %w", err)
	}

	// Initialize command router
	router := commands.NewRouter("/ruriko")
	handlers := commands.NewHandlers(store, secretsStore)

	// Register command handlers
	router.Register("help", handlers.HandleHelp)
	router.Register("version", handlers.HandleVersion)
	router.Register("ping", handlers.HandlePing)
	router.Register("agents.list", handlers.HandleAgentsList)
	router.Register("agents.show", handlers.HandleAgentsShow)
	router.Register("audit.tail", handlers.HandleAuditTail)
	router.Register("trace", handlers.HandleTrace)
	router.Register("secrets.list", handlers.HandleSecretsList)
	router.Register("secrets.set", handlers.HandleSecretsSet)
	router.Register("secrets.info", handlers.HandleSecretsInfo)
	router.Register("secrets.rotate", handlers.HandleSecretsRotate)
	router.Register("secrets.delete", handlers.HandleSecretsDelete)
	router.Register("secrets.bind", handlers.HandleSecretsBind)
	router.Register("secrets.unbind", handlers.HandleSecretsUnbind)

	return &App{
		config:   config,
		store:    store,
		secrets:  secretsStore,
		matrix:   matrixClient,
		router:   router,
		handlers: handlers,
	}, nil
}

// Run starts the Ruriko application
func (a *App) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Matrix client
	fmt.Println("Starting Matrix sync...")
	if err := a.matrix.Start(ctx, a.handleMessage); err != nil {
		return fmt.Errorf("failed to start Matrix client: %w", err)
	}

	// Send startup message to admin rooms
	for _, roomID := range a.config.Matrix.AdminRooms {
		a.matrix.SendNotice(roomID, "✅ Ruriko control plane started. Type /ruriko help for commands.")
	}

	fmt.Println("Ruriko is running. Press Ctrl+C to stop.")

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down...")
	return nil
}

// Stop stops the Ruriko application
func (a *App) Stop() {
	fmt.Println("Stopping Matrix client...")
	a.matrix.Stop()

	fmt.Println("Closing database...")
	a.store.Close()
}

// handleMessage processes incoming Matrix messages
func (a *App) handleMessage(ctx context.Context, evt *event.Event) {
	msgContent := evt.Content.AsMessage()
	if msgContent == nil {
		return
	}

	text := msgContent.Body

	// Try to route the command
	response, err := a.router.Route(ctx, text, evt)
	if err != nil {
		// Not a command or error
		if err.Error() != "not a command (missing prefix)" {
			a.matrix.ReplyToMessage(evt.RoomID.String(), evt.ID.String(), fmt.Sprintf("❌ Error: %s", err))
		}
		return
	}

	// Send response
	if response != "" {
		if err := a.matrix.SendMessage(evt.RoomID.String(), response); err != nil {
			fmt.Printf("Failed to send response: %v\n", err)
		}
	}
}
