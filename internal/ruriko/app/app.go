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

	"github.com/bdobrica/Ruriko/internal/ruriko/approvals"
	"github.com/bdobrica/Ruriko/internal/ruriko/audit"
	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	rurikoconfig "github.com/bdobrica/Ruriko/internal/ruriko/config"
	"github.com/bdobrica/Ruriko/internal/ruriko/kuze"
	"github.com/bdobrica/Ruriko/internal/ruriko/matrix"
	"github.com/bdobrica/Ruriko/internal/ruriko/memory"
	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
	"github.com/bdobrica/Ruriko/internal/ruriko/provisioning"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/docker"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
	"github.com/bdobrica/Ruriko/internal/ruriko/webhook"
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
	// HTTPAddr is the TCP address for the optional health/status HTTP server
	// (e.g. ":8080"). When empty the server is disabled.
	HTTPAddr string
	// KuzeBaseURL is the externally reachable base URL of the Ruriko HTTP
	// server (e.g. "https://ruriko.example.com"). When non-empty and HTTPAddr
	// is also set, the Kuze one-time-link routes are mounted on the HTTP
	// server and the /ruriko secrets set command issues links instead of
	// accepting inline values.
	KuzeBaseURL string
	// KuzeTTL is the lifetime of Kuze one-time tokens. Defaults to 10 minutes
	// when zero.
	KuzeTTL time.Duration
	// AuditRoomID is an optional Matrix room ID (e.g. "!abc:example.com") where
	// Ruriko posts human-friendly summaries of major control-plane events.
	// When empty, audit room notifications are disabled.
	AuditRoomID string
	// DefaultAgentImage is the container image used for agents created through
	// the natural-language provisioning wizard (R5.4 stretch goal).
	// When empty, "ghcr.io/bdobrica/gitai:latest" is used as a fallback.
	DefaultAgentImage string

	// WebhookRateLimit is the maximum number of inbound webhook deliveries
	// accepted per agent per minute before Ruriko starts returning 429.
	// Defaults to webhook.DefaultRateLimit (60) when zero.
	WebhookRateLimit int

	// --- R9: Natural Language Interface ---

	// NLPProvider is an optional pre-constructed LLM provider for natural-
	// language command classification.  When non-nil it is used directly and
	// the NLPModel/NLPEndpoint/NLPAPIKeySecretRef fields are ignored.
	// When nil the app auto-constructs an OpenAI-compatible provider from the
	// fields below, provided an API key is present in the environment.
	// Setting this to nil when no key is configured leaves the NL layer in the
	// deterministic keyword-matching mode introduced in R5.4.
	NLPProvider nlp.Provider

	// NLPModel is the chat model used for intent classification.
	// Defaults to "gpt-4o-mini" (cost-efficient) when empty.
	NLPModel string

	// NLPEndpoint is the base URL of the LLM API endpoint, e.g.:
	//   https://api.openai.com/v1  (default)
	//   http://localhost:11434/v1  (Ollama)
	//   https://<resource>.openai.azure.com/openai/deployments/<deployment>
	// Empty defaults to the public OpenAI endpoint.
	NLPEndpoint string

	// NLPAPIKeySecretRef is the name of the environment variable that holds
	// the API key for the NLP provider.  Defaults to "RURIKO_NLP_API_KEY".
	// The key is always read from the environment — never from Matrix chat.
	NLPAPIKeySecretRef string

	// NLPRateLimit is the maximum number of NLP classification calls allowed
	// per sender per minute.  Defaults to nlp.DefaultRateLimit (20) when zero.
	NLPRateLimit int

	// NLPTokenBudget is the maximum number of LLM tokens allowed per sender
	// per UTC day.  Defaults to nlp.DefaultTokenBudget (50 000) when zero.
	// Set the NLP_TOKEN_BUDGET environment variable to override.
	NLPTokenBudget int

	// --- R10: Conversation Memory ---

	// MemoryEnabled, when true, forces the conversation memory subsystem on
	// even when no NLP provider is configured.  When false (the default), memory
	// is automatically enabled whenever an NLP provider is available (either via
	// NLPProvider or an API key in the environment).
	MemoryEnabled bool

	// MemoryCooldown is the duration of inactivity after which a conversation
	// is sealed and archived to long-term memory.  Defaults to 15 minutes when
	// zero.
	MemoryCooldown time.Duration

	// MemorySTMMaxMessages is the maximum number of messages retained in the
	// short-term buffer per conversation.  When exceeded, the oldest messages
	// are dropped (sliding window).  Defaults to 50 when zero.
	MemorySTMMaxMessages int

	// MemorySTMMaxTokens is the estimated token budget for the short-term
	// buffer per conversation.  When exceeded, the oldest messages are dropped
	// until the estimate is under budget.  Defaults to 8000 when zero.
	MemorySTMMaxTokens int

	// MemoryLTMTopK is the number of long-term memory entries retrieved per
	// classification call when an embedding-capable backend is configured.
	// Defaults to 3 when zero.
	MemoryLTMTopK int

	// --- R10.7: Persistent Memory Backends ---

	// MemoryLTMBackend selects the long-term memory storage backend.
	// Supported values: "noop" (default), "sqlite".
	// When "sqlite", the Ruriko database is used for LTM storage.
	MemoryLTMBackend string

	// MemoryEmbeddingAPIKey is the API key for the OpenAI-compatible
	// embedding provider. When non-empty, enables real embedding-based
	// similarity search in LTM. Uses the same key as the NLP provider by
	// default (read from RURIKO_MEMORY_EMBEDDING_API_KEY or fallback to
	// RURIKO_NLP_API_KEY).
	MemoryEmbeddingAPIKey string

	// MemoryEmbeddingEndpoint overrides the embedding API base URL.
	// Defaults to https://api.openai.com/v1 when empty.
	MemoryEmbeddingEndpoint string

	// MemoryEmbeddingModel overrides the embedding model name.
	// Defaults to "text-embedding-3-small" when empty.
	MemoryEmbeddingModel string

	// MemorySummariserAPIKey is the API key for the LLM-based summariser.
	// When non-empty, enables LLM-powered conversation summarisation.
	// Falls back to MemoryEmbeddingAPIKey, then RURIKO_NLP_API_KEY.
	MemorySummariserAPIKey string

	// MemorySummariserEndpoint overrides the summariser API base URL.
	// Defaults to https://api.openai.com/v1 when empty.
	MemorySummariserEndpoint string

	// MemorySummariserModel overrides the summariser model name.
	// Defaults to "gpt-4o-mini" when empty.
	MemorySummariserModel string
}

// App is the main Ruriko application
type App struct {
	config       *Config
	store        *store.Store
	secrets      *secrets.Store
	configStore  rurikoconfig.Store
	matrix       *matrix.Client
	router       *commands.Router
	handlers     *commands.Handlers
	reconciler   *runtime.Reconciler
	healthServer *HealthServer
	kuzeServer   *kuze.Server
	webhookProxy *webhook.Proxy
	sealRunner   *memory.SealPipelineRunner
}

// kuzeTokenAdapter bridges *kuze.Server → secrets.TokenIssuer, breaking the
// circular import between the secrets and kuze packages. The adapter converts
// *kuze.AgentIssueResult → *secrets.TokenLeaseResult.
type kuzeTokenAdapter struct {
	srv *kuze.Server
}

func (a *kuzeTokenAdapter) IssueAgentToken(ctx context.Context, agentID, secretRef, secretType, purpose string) (*secrets.TokenLeaseResult, error) {
	r, err := a.srv.IssueAgentToken(ctx, agentID, secretRef, secretType, purpose)
	if err != nil {
		return nil, err
	}
	return &secrets.TokenLeaseResult{
		RedeemURL: r.RedeemURL,
		SecretRef: r.SecretRef,
		Token:     r.Token,
	}, nil
}

// New creates a new Ruriko application
func New(config *Config) (*App, error) {
	// Initialize database
	slog.Info("opening database", "path", config.DatabasePath)
	store, err := store.New(config.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Initialize Matrix client.
	// Inject the DB so the client can persist the sync token across restarts.
	matrixCfg := config.Matrix
	matrixCfg.DB = store.DB()
	slog.Info("connecting to Matrix", "homeserver", matrixCfg.Homeserver)
	matrixClient, err := matrix.New(&matrixCfg)
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

	// Initialize runtime config store (non-secret key/value knobs such as
	// nlp.model and nlp.endpoint, managed via /ruriko config).
	configStore := rurikoconfig.New(store)
	slog.Info("runtime config store ready")

	// Initialize command router
	router := commands.NewRouter("/ruriko")

	// Build the handlers configuration progressively; optional subsystems
	// are attached only when their prerequisites are met.
	handlersCfg := commands.HandlersConfig{
		Store:            store,
		Secrets:          secretsStore,
		ConfigStore:      configStore,
		MatrixHomeserver: config.Matrix.Homeserver,
	}

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
			handlersCfg.Runtime = dockerAdapter
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
			handlersCfg.Provisioner = p
			slog.Info("Matrix provisioner ready", "type", config.Provisioning.HomeserverType)
		}
	}

	// Initialise Kuze secret-entry server when both HTTPAddr and KuzeBaseURL
	// are configured. Kuze is created before the distributor and handlers so
	// that (a) the distributor can use token-based distribution and (b) the
	// handlers receive a non-nil Kuze reference.
	var kuzeServer *kuze.Server
	if config.HTTPAddr != "" && config.KuzeBaseURL != "" {
		kuzeServer = kuze.New(store.DB(), secretsStore, kuze.Config{
			BaseURL: config.KuzeBaseURL,
			TTL:     config.KuzeTTL,
		})
		kuzeServer.SetSecretsGetter(secretsStore)
		handlersCfg.Kuze = kuzeServer
		slog.Info("Kuze secret-entry server ready", "baseURL", config.KuzeBaseURL)

		// Wire Matrix notifications for Kuze events.  Store confirmations and
		// expiry notices are sent to all configured admin rooms so the operator
		// is kept in the loop without polling.
		adminRooms := config.Matrix.AdminRooms
		kuzeServer.SetOnSecretStored(func(ctx context.Context, secretRef string) {
			msg := fmt.Sprintf("✓ Secret **%s** stored securely.", secretRef)
			for _, roomID := range adminRooms {
				if err := matrixClient.SendNotice(roomID, msg); err != nil {
					slog.Warn("kuze: send store-confirmation to Matrix",
						"room", roomID, "ref", secretRef, "err", err)
				}
			}
		})

		kuzeServer.SetOnTokenExpired(func(ctx context.Context, pt *kuze.PendingToken) {
			msg := fmt.Sprintf(
				"⏰ The one-time link for secret **%s** has expired without being used. "+
					"Use `/ruriko secrets set %s` to generate a new link.",
				pt.SecretRef, pt.SecretRef,
			)
			for _, roomID := range adminRooms {
				if err := matrixClient.SendNotice(roomID, msg); err != nil {
					slog.Warn("kuze: send expiry notification to Matrix",
						"room", roomID, "ref", pt.SecretRef, "err", err)
				}
			}
		})
	}

	// Initialise secrets distributor. When Kuze is available, the distributor
	// issues one-time tokens so plaintext secrets never traverse ACP payloads.
	var distributor *secrets.Distributor
	if kuzeServer != nil {
		distributor = secrets.NewDistributorWithKuze(secretsStore, store, &kuzeTokenAdapter{srv: kuzeServer})
		slog.Info("secrets distributor ready (token-based via Kuze)")
	} else {
		distributor = secrets.NewDistributor(secretsStore, store)
		slog.Info("secrets distributor ready (legacy direct push)")
	}
	handlersCfg.Distributor = distributor

	// Initialise template registry if a templates FS is provided.
	if config.TemplatesFS != nil {
		reg := templates.NewRegistry(config.TemplatesFS)
		handlersCfg.Templates = reg
		slog.Info("Gosuto template registry ready")
	}

	// Initialise NLP provider for natural-language command classification (R9).
	//
	// R9.7 lookup order (evaluated lazily on every Classify call):
	//   1. "ruriko.nlp-api-key" secret in the encrypted secrets store (preferred)
	//   2. Env var named by NLPAPIKeySecretRef / RURIKO_NLP_API_KEY (bootstrap)
	//   3. Neither → NL layer uses the deterministic keyword matcher (R5.4)
	//
	// A pre-constructed Config.NLPProvider short-circuits key resolution and is
	// used as-is (useful for integration tests and custom provider wiring).
	{
		if config.NLPProvider != nil {
			handlersCfg.NLPProvider = config.NLPProvider
			slog.Info("NLP: using pre-configured provider (key resolution disabled)")
		} else {
			envVar := config.NLPAPIKeySecretRef
			if envVar == "" {
				envVar = "RURIKO_NLP_API_KEY"
			}
			if apiKey := os.Getenv(envVar); apiKey != "" {
				// Capture the env-var key as the bootstrap fallback so that
				// resolveNLPProvider (R9.7) can use it before the operator has
				// stored the secret via `/ruriko secrets set ruriko.nlp-api-key`.
				handlersCfg.NLPEnvAPIKey = apiKey
				slog.Info("NLP: bootstrap API key found in environment; provider will be built lazily",
					"key_env", envVar,
					"hint", "store via /ruriko secrets set ruriko.nlp-api-key for secure rotation")
			} else {
				slog.Info("NLP: no API key in environment; keyword matching active until a secret is stored",
					"key_env", envVar,
					"secret_name", "ruriko.nlp-api-key")
			}
		}

		// Rate-limiter and token budget are always initialised so they take
		// effect the moment a key becomes available (env var or secrets store).
		rateLimit := config.NLPRateLimit
		rateLimiter := nlp.NewRateLimiter(rateLimit, time.Minute)
		tokenBudget := nlp.NewTokenBudget(config.NLPTokenBudget)
		handlersCfg.NLPRateLimiter = rateLimiter
		handlersCfg.NLPTokenBudget = tokenBudget
		slog.Info("NLP: rate-limiter and token budget ready", "daily_tokens_per_sender", tokenBudget.Budget())
	}

	// Initialise approval gate.
	approvalsStore := approvals.NewStore(store.DB())
	approvalsGate := approvals.NewGate(approvalsStore, 0 /* default TTL */)
	handlersCfg.Approvals = approvalsGate
	slog.Info("approval workflow ready")

	// Initialise audit room notifier.
	var notifier audit.Notifier = audit.Noop{}
	if config.AuditRoomID != "" {
		notifier = audit.NewMatrixNotifier(matrixClient, config.AuditRoomID)
		slog.Info("audit room notifier ready", "room", config.AuditRoomID)
	}
	handlersCfg.Notifier = notifier

	// Wire the Matrix client as the RoomSender so that the async
	// provisioning pipeline (R5.2) can post breadcrumb notices back to the
	// operator's admin room while each step is running.
	handlersCfg.RoomSender = matrixClient

	// Wire the default agent image for the natural-language wizard (R5.4).
	handlersCfg.DefaultAgentImage = config.DefaultAgentImage

	// --- R10: Conversation memory -------------------------------------------
	// Wire the memory subsystem when the NLP provider is available (or will
	// become available via lazy rebuild), or when explicitly enabled via
	// Config.MemoryEnabled. The memory layer uses noop backends by default —
	// summarisation, embedding, and LTM persistence are pluggable and will
	// produce meaningful results when real backends are configured.
	//
	// Auto-detection: if an NLP API key or provider is present in the config
	// the memory subsystem is initialised automatically, matching the
	// documented default of "true when NLP provider is configured".
	var sealRunner *memory.SealPipelineRunner
	{
		// Determine the NLP env-var name so we can check availability.
		nlpEnvVar := config.NLPAPIKeySecretRef
		if nlpEnvVar == "" {
			nlpEnvVar = "RURIKO_NLP_API_KEY"
		}
		nlpAvailable := config.NLPProvider != nil || os.Getenv(nlpEnvVar) != ""

		shouldEnableMemory := config.MemoryEnabled || nlpAvailable

		if !shouldEnableMemory {
			slog.Info("conversation memory disabled (no NLP provider configured; set MemoryEnabled to force on)")
			// handlersCfg.Memory and handlersCfg.SealPipeline remain nil.
		} else {
			trackerCfg := memory.DefaultTrackerConfig()
			if config.MemoryCooldown > 0 {
				trackerCfg.Cooldown = config.MemoryCooldown
			}
			if config.MemorySTMMaxMessages > 0 {
				trackerCfg.MaxMessages = config.MemorySTMMaxMessages
			}
			if config.MemorySTMMaxTokens > 0 {
				trackerCfg.MaxTokens = config.MemorySTMMaxTokens
			}

			ltmTopK := memory.DefaultLTMTopK
			if config.MemoryLTMTopK > 0 {
				ltmTopK = config.MemoryLTMTopK
			}

			tracker := memory.NewTracker(trackerCfg)

			// --- Select memory backends (R10.7) ---
			var summariser memory.Summariser = memory.NoopSummariser{}
			var embedder memory.Embedder = memory.NoopEmbedder{}
			var ltm memory.LongTermMemory = memory.NewNoopLTM(slog.Default())

			backendLabel := "noop"

			// LTM backend: SQLite uses the existing Ruriko database.
			if config.MemoryLTMBackend == "sqlite" {
				ltm = memory.NewSQLiteLTM(store.DB(), slog.Default())
				backendLabel = "sqlite"
				slog.Info("ltm backend: sqlite (using Ruriko database)")
			}

			// Embedder: OpenAI-compatible API when a key is available.
			embAPIKey := resolveMemoryAPIKey(config.MemoryEmbeddingAPIKey, config.NLPAPIKeySecretRef)
			if embAPIKey != "" {
				embedder = memory.NewOpenAIEmbedder(memory.OpenAIEmbedderConfig{
					APIKey:  embAPIKey,
					BaseURL: config.MemoryEmbeddingEndpoint,
					Model:   config.MemoryEmbeddingModel,
				})
				slog.Info("embedder backend: openai",
					"model", orDefault(config.MemoryEmbeddingModel, "text-embedding-3-small"),
				)
			}

			// Summariser: LLM-based when a key is available.
			sumAPIKey := resolveMemoryAPIKey(config.MemorySummariserAPIKey, config.NLPAPIKeySecretRef)
			if sumAPIKey == "" {
				sumAPIKey = embAPIKey // fall back to embedding key
			}
			if sumAPIKey != "" {
				summariser = memory.NewLLMSummariser(memory.LLMSummariserConfig{
					APIKey:  sumAPIKey,
					BaseURL: config.MemorySummariserEndpoint,
					Model:   config.MemorySummariserModel,
				})
				slog.Info("summariser backend: llm",
					"model", orDefault(config.MemorySummariserModel, "gpt-4o-mini"),
				)
			}

			assembler := &memory.ContextAssembler{
				STM:       tracker,
				LTM:       ltm,
				Embedder:  embedder,
				MaxTokens: memory.DefaultMaxTokens,
				LTMTopK:   ltmTopK,
			}
			handlersCfg.Memory = assembler

			pipeline := memory.NewSealPipeline(summariser, embedder, ltm, slog.Default())
			handlersCfg.SealPipeline = pipeline

			sealRunner = memory.NewSealPipelineRunner(tracker, pipeline, 60*time.Second, slog.Default())
			slog.Info("conversation memory ready",
				"ltm_backend", backendLabel,
				"cooldown", trackerCfg.Cooldown,
				"stm_max_messages", trackerCfg.MaxMessages,
				"stm_max_tokens", trackerCfg.MaxTokens,
				"ltm_top_k", ltmTopK,
			)
		}
	}

	handlers := commands.NewHandlers(handlersCfg)

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
	router.Register("agents.cancel", handlers.HandleAgentsCancel)
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
	router.Register("gosuto.set-instructions", handlers.HandleGosutoSetInstructions)
	router.Register("gosuto.set-persona", handlers.HandleGosutoSetPersona)
	router.Register("approvals.list", handlers.HandleApprovalsList)
	router.Register("approvals.show", handlers.HandleApprovalsShow)
	router.Register("config.set", handlers.HandleConfigSet)
	router.Register("config.get", handlers.HandleConfigGet)
	router.Register("config.list", handlers.HandleConfigList)
	router.Register("config.unset", handlers.HandleConfigUnset)

	// Wire the dispatch callback so approved operations can be re-executed.
	handlers.SetDispatch(func(ctx context.Context, action string, cmd *commands.Command, evt *event.Event) (string, error) {
		return router.Dispatch(ctx, action, cmd, evt)
	})

	// Optionally build the health/status HTTP server.
	var healthServer *HealthServer
	var webhookProxy *webhook.Proxy
	if config.HTTPAddr != "" {
		healthServer = NewHealthServer(config.HTTPAddr, store)
		healthServer.SetNLPStatusProvider(handlers)
		if kuzeServer != nil {
			kuzeServer.RegisterRoutes(healthServer)
			slog.Info("Kuze routes registered on HTTP server")
		}
		// Mount the webhook reverse proxy (R13.1): POST /webhooks/{agent}/{source}
		webhookProxy = webhook.New(store, secretsStore, webhook.Config{
			RateLimit: config.WebhookRateLimit,
		})
		webhookProxy.RegisterRoutes(healthServer)
		slog.Info("webhook reverse proxy registered on HTTP server")
		slog.Info("health server configured", "addr", config.HTTPAddr)
	}

	return &App{
		config:       config,
		store:        store,
		secrets:      secretsStore,
		configStore:  configStore,
		matrix:       matrixClient,
		router:       router,
		handlers:     handlers,
		reconciler:   reconciler,
		healthServer: healthServer,
		kuzeServer:   kuzeServer,
		webhookProxy: webhookProxy,
		sealRunner:   sealRunner,
	}, nil
}

// Run starts the Ruriko application
func (a *App) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start health/status HTTP server if configured.
	if a.healthServer != nil {
		if err := a.healthServer.Start(ctx); err != nil {
			slog.Warn("health server failed to start; continuing without it", "err", err)
		}
	}

	// Start Matrix client
	slog.Info("starting Matrix sync")
	if err := a.matrix.Start(ctx, a.handleMessage); err != nil {
		return fmt.Errorf("failed to start Matrix client: %w", err)
	}

	// Start reconciler in background if configured
	if a.reconciler != nil {
		go a.reconciler.Run(ctx)
	}

	// Start conversation seal-check runner (R10.5).  Periodically scans for
	// conversations past their cooldown and processes them through the
	// summarise → embed → store LTM pipeline.
	if a.sealRunner != nil {
		go a.sealRunner.Run(ctx)
	}

	// Start Kuze token-pruning loop.  Expired tokens are detected, Matrix
	// expiry notifications are sent, then the rows are deleted.  The loop
	// runs on the same cadence as KuzeTTL (defaulting to kuze.DefaultTTL).
	if a.kuzeServer != nil {
		go func() {
			interval := a.config.KuzeTTL
			if interval <= 0 {
				interval = kuze.DefaultTTL
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := a.kuzeServer.PruneExpiredWithNotify(ctx); err != nil {
						slog.Warn("kuze: prune expired tokens", "err", err)
					}
				}
			}
		}()
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

	if a.healthServer != nil {
		slog.Info("stopping health server")
		a.healthServer.Stop()
	}

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

	// Secret-in-chat guardrail: refuse to process any message that appears to
	// contain a sensitive credential.  The guardrail is active only when Kuze
	// is configured (production mode); in dev/test mode inline secrets are
	// allowed as a deliberate fallback.
	//
	// For non-command messages all patterns (named + generic) are checked.
	// For command messages only named vendor patterns (OpenAI sk-…, AWS AKIA…,
	// etc.) are checked so that legitimate base64 command arguments like
	// gosuto --content ... are not falsely rejected.
	if a.kuzeServer != nil {
		isCmd := strings.HasPrefix(text, "/ruriko")
		if commands.LooksLikeSecret(text, isCmd) {
			a.matrix.ReplyToMessage(
				evt.RoomID.String(), evt.ID.String(),
				commands.SecretGuardrailMessage,
			)
			return
		}
	}

	// Try to route the command
	response, err := a.router.Route(ctx, text, evt)
	if err != nil {
		if errors.Is(err, commands.ErrNotACommand) {
			// Not a /ruriko command — first check if it's an approval decision
			// (approve <id> / deny <id> reason=...).
			decisionResp, decisionErr := a.handlers.HandleApprovalDecision(ctx, text, evt)
			if decisionErr != nil {
				if !errors.Is(decisionErr, approvals.ErrNotADecision) {
					// It parsed as a decision but failed — report the error.
					a.matrix.ReplyToMessage(evt.RoomID.String(), evt.ID.String(),
						fmt.Sprintf("❌ Error: %s", decisionErr))
				}
				// ErrNotADecision — fall through to natural-language handler.
			} else if decisionResp != "" {
				htmlBody := markdownToHTML(decisionResp)
				if err2 := a.matrix.SendFormattedMessage(evt.RoomID.String(), htmlBody, decisionResp); err2 != nil {
					slog.Error("failed to send approval response", "room", evt.RoomID.String(), "err", err2)
				}
				return
			}

			// Natural-language intent detection (R5.4).
			// Active only when a template registry is configured (i.e. a production
			// deployment with templates on disk or embedded).  In dev mode with no
			// templates the handler is a no-op and returns ("", nil).
			if nlResp, nlErr := a.handlers.HandleNaturalLanguage(ctx, text, evt); nlErr != nil {
				a.matrix.ReplyToMessage(evt.RoomID.String(), evt.ID.String(),
					fmt.Sprintf("❌ Error: %s", nlErr))
			} else if nlResp != "" {
				htmlBody := markdownToHTML(nlResp)
				if err2 := a.matrix.SendFormattedMessage(evt.RoomID.String(), htmlBody, nlResp); err2 != nil {
					slog.Error("failed to send NL response", "room", evt.RoomID.String(), "err", err2)
				}
			}
			// else: ordinary chat message — ignore silently.
			return
		}
		// A /ruriko-prefixed command that errored.
		a.matrix.ReplyToMessage(evt.RoomID.String(), evt.ID.String(), fmt.Sprintf("❌ Error: %s", err))
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

// resolveMemoryAPIKey returns the first non-empty value from: the explicit
// key, the environment variable named by envRef, or the RURIKO_NLP_API_KEY
// environment variable. This provides a natural fallback chain so users
// only need to configure one API key when the same provider serves NLP,
// embeddings, and summarisation.
func resolveMemoryAPIKey(explicit, envRef string) string {
	if explicit != "" {
		return explicit
	}
	if envRef != "" {
		if v := os.Getenv(envRef); v != "" {
			return v
		}
	}
	return os.Getenv("RURIKO_NLP_API_KEY")
}

// orDefault returns s if non-empty, otherwise fallback.
func orDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
