// Package app provides the main Ruriko application
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/matrix"
	"github.com/bdobrica/Ruriko/internal/ruriko/provisioning"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/docker"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
)

// Config holds application configuration
type Config struct {
	DatabasePath      string
	MasterKey         []byte
	Matrix            matrix.Config
	EnableDocker      bool
	DockerNetwork     string
	ReconcileInterval time.Duration
	// AdminSenders is an optional allowlist of Matrix user IDs (e.g. "@alice:example.com")
	// permitted to execute commands. When empty, any room member can send commands.
	AdminSenders []string
	// Provisioning holds optional Matrix account provisioning configuration.
	// When nil, the agents.matrix.register command will require --mxid.
	Provisioning *provisioning.Config
	// TemplatesFS is an optional filesystem rooted at the templates directory.
	// When non-nil, Gosuto template commands are enabled.  Pass os.DirFS(path)
	// or an embed.FS sub-tree.
	TemplatesFS fs.FS
}

// App is the main Ruriko application
type App struct {
	config     *Config
	store      *store.Store
	secrets    *secrets.Store
	matrix     *matrix.Client
	router     *commands.Router
	handlers   *commands.Handlers
	reconciler *runtime.Reconciler
}

// New creates a new Ruriko application
func New(config *Config) (*App, error) {
	// Initialize database
	slog.Info("opening database", "path", config.DatabasePath)
	store, err := store.New(config.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Initialize Matrix client
	slog.Info("connecting to Matrix", "homeserver", config.Matrix.Homeserver)
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

	// Initialize Docker runtime if enabled
	var reconciler *runtime.Reconciler
	if config.EnableDocker {
		networkName := config.DockerNetwork
		if networkName == "" {
			networkName = runtime.DefaultNetwork
		}
		dockerAdapter, err := docker.NewWithNetwork(networkName)
		if err != nil {
			slog.Warn("Docker runtime unavailable", "err", err)
		} else {
			// Ensure the Ruriko bridge network exists before spawning any containers.
			if netErr := dockerAdapter.EnsureNetwork(context.Background()); netErr != nil {
				slog.Warn("could not ensure Docker network; agent spawning may fail", "network", networkName, "err", netErr)
			}
			handlers.SetRuntime(dockerAdapter)
			reconcileInterval := config.ReconcileInterval
			if reconcileInterval == 0 {
				reconcileInterval = 30 * time.Second
			}
			reconciler = runtime.NewReconciler(dockerAdapter, store, runtime.ReconcilerConfig{
				Interval: reconcileInterval,
			})
		}
	}

	// Initialise Matrix provisioner if configured.
	if config.Provisioning != nil {
		p, err := provisioning.New(*config.Provisioning)
		if err != nil {
			slog.Warn("Matrix provisioner unavailable; agents.matrix.register will require --mxid",
				"err", err)
		} else {
			handlers.SetProvisioner(p)
			slog.Info("Matrix provisioner ready", "type", config.Provisioning.HomeserverType)
		}
	}

	// Initialise secrets distributor.
	distributor := secrets.NewDistributor(secretsStore, store)
	handlers.SetDistributor(distributor)

	// Initialise template registry if a templates FS is provided.
	if config.TemplatesFS != nil {
		reg := templates.NewRegistry(config.TemplatesFS)
		handlers.SetTemplates(reg)
		slog.Info("Gosuto template registry ready")
	}

	// Register command handlers
	router.Register("help", handlers.HandleHelp)
	router.Register("version", handlers.HandleVersion)
	router.Register("ping", handlers.HandlePing)
	router.Register("agents.list", handlers.HandleAgentsList)
	router.Register("agents.show", handlers.HandleAgentsShow)
	router.Register("agents.create", handlers.HandleAgentsCreate)
	router.Register("agents.stop", handlers.HandleAgentsStop)
	router.Register("agents.start", handlers.HandleAgentsStart)
	router.Register("agents.respawn", handlers.HandleAgentsRespawn)
	router.Register("agents.delete", handlers.HandleAgentsDelete)
	router.Register("agents.status", handlers.HandleAgentsStatus)
	router.Register("agents.matrix", handlers.HandleAgentsMatrixRegister)
	router.Register("agents.disable", handlers.HandleAgentsDisable)
	router.Register("audit.tail", handlers.HandleAuditTail)
	router.Register("trace", handlers.HandleTrace)
	router.Register("secrets.list", handlers.HandleSecretsList)
	router.Register("secrets.set", handlers.HandleSecretsSet)
	router.Register("secrets.info", handlers.HandleSecretsInfo)
	router.Register("secrets.rotate", handlers.HandleSecretsRotate)
	router.Register("secrets.delete", handlers.HandleSecretsDelete)
	router.Register("secrets.bind", handlers.HandleSecretsBind)
	router.Register("secrets.unbind", handlers.HandleSecretsUnbind)
	router.Register("secrets.push", handlers.HandleSecretsPush)
	router.Register("gosuto.show", handlers.HandleGosutoShow)
	router.Register("gosuto.versions", handlers.HandleGosutoVersions)
	router.Register("gosuto.diff", handlers.HandleGosutoDiff)
	router.Register("gosuto.set", handlers.HandleGosutoSet)
	router.Register("gosuto.rollback", handlers.HandleGosutoRollback)
	router.Register("gosuto.push", handlers.HandleGosutoPush)

	return &App{
		config:     config,
		store:      store,
		secrets:    secretsStore,
		matrix:     matrixClient,
		router:     router,
		handlers:   handlers,
		reconciler: reconciler,
	}, nil
}

// Run starts the Ruriko application
func (a *App) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Matrix client
	slog.Info("starting Matrix sync")
	if err := a.matrix.Start(ctx, a.handleMessage); err != nil {
		return fmt.Errorf("failed to start Matrix client: %w", err)
	}

	// Start reconciler in background if configured
	if a.reconciler != nil {
		go a.reconciler.Run(ctx)
	}

	// Send startup message to admin rooms
	for _, roomID := range a.config.Matrix.AdminRooms {
		a.matrix.SendNotice(roomID, "✅ Ruriko control plane started. Type /ruriko help for commands.")
	}

	slog.Info("Ruriko is running; press Ctrl+C to stop")

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down")
	return nil
}

// Stop stops the Ruriko application
func (a *App) Stop() {
	slog.Info("stopping Matrix client")
	a.matrix.Stop()

	slog.Info("closing database")
	a.store.Close()
}

// handleMessage processes incoming Matrix messages
func (a *App) handleMessage(ctx context.Context, evt *event.Event) {
	msgContent := evt.Content.AsMessage()
	if msgContent == nil {
		return
	}

	// Enforce sender allowlist when configured
	if len(a.config.AdminSenders) > 0 {
		sender := evt.Sender.String()
		allowed := false
		for _, s := range a.config.AdminSenders {
			if s == sender {
				allowed = true
				break
			}
		}
		if !allowed {
			// Silently ignore commands from users not on the allowlist
			return
		}
	}

	text := msgContent.Body

	// Try to route the command
	response, err := a.router.Route(ctx, text, evt)
	if err != nil {
		// Not a command or error
		if !errors.Is(err, commands.ErrNotACommand) {
			a.matrix.ReplyToMessage(evt.RoomID.String(), evt.ID.String(), fmt.Sprintf("❌ Error: %s", err))
		}
		return
	}

	// Send response — use the formatted variant so Markdown syntax (bold, code
	// blocks, etc.) is rendered by Matrix clients that support HTML messages.
	if response != "" {
		htmlBody := markdownToHTML(response)
		if err := a.matrix.SendFormattedMessage(evt.RoomID.String(), htmlBody, response); err != nil {
			slog.Error("failed to send response", "room", evt.RoomID.String(), "err", err)
		}
	}
}

// markdownToHTML converts the small subset of Markdown produced by Ruriko
// command handlers into HTML suitable for a Matrix m.text event with
// format=org.matrix.custom.html.
//
// Supported constructs (in order of processing):
//   - Fenced code blocks  ```…```  → <pre><code>…</code></pre>
//   - Inline code  `…`             → <code>…</code>
//   - Bold  **…**                  → <strong>…</strong>
//   - Newlines                     → <br/>
func markdownToHTML(md string) string {
	// Process fenced code blocks first so their content is not touched by
	// subsequent inline passes.
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inCode := false
	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if !inCode {
				out.WriteString("<pre><code>")
				inCode = true
			} else {
				out.WriteString("</code></pre>")
				inCode = false
			}
			continue
		}
		if inCode {
			// Escape HTML entities inside code blocks.
			escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(line)
			out.WriteString(escaped)
			out.WriteString("\n")
		} else {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}
	result := out.String()

	// Inline code: `…`
	result = replaceDelimited(result, "`", "<code>", "</code>")

	// Bold: **…**
	result = replaceDelimited(result, "**", "<strong>", "</strong>")

	// Convert bare newlines to <br/>.
	result = strings.ReplaceAll(result, "\n", "<br/>")

	return result
}

// replaceDelimited replaces occurrences of delim…delim with open+content+close.
// Only complete pairs are replaced; an unmatched opener is left as-is.
func replaceDelimited(s, delim, open, close string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, delim)
		if start == -1 {
			b.WriteString(s)
			break
		}
		end := strings.Index(s[start+len(delim):], delim)
		if end == -1 {
			b.WriteString(s)
			break
		}
		end += start + len(delim) // absolute index of closing delim
		b.WriteString(s[:start])
		b.WriteString(open)
		b.WriteString(s[start+len(delim) : end])
		b.WriteString(close)
		s = s[end+len(delim):]
	}
	return b.String()
}
