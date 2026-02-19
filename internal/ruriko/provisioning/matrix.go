// Package provisioning handles Matrix account lifecycle for agents.
//
// It supports four registration strategies selected by the HomeserverType field:
//
//   - "tuwunel" – Tuwunel (and compatible conduwuit-based) homeservers.  Uses the
//     standard Matrix client-server registration endpoint.  If RegistrationToken
//     is set the "m.login.registration_token" flow is used; otherwise the
//     "m.login.dummy" open-registration flow is used.  Tuwunel is the default.
//   - "synapse" – Synapse shared-secret registration API (recommended for
//     self-hosted Synapse deployments).  Requires SharedSecret to be set.
//   - "generic" – Standard Matrix client-server registration endpoint with the
//     dummy auth flow.  Only works when open registration is enabled on the
//     homeserver.
//   - "manual" – No automatic registration; the caller must supply an existing
//     MXID via the --mxid flag when creating an agent.
//
// Deprovisioning uses the Synapse admin deactivate API when the homeserver type
// is "synapse", and is a no-op (warning only) for other types.
package provisioning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/synapseadmin"

	"github.com/bdobrica/Ruriko/common/trace"
)

// HomeserverType selects the registration strategy.
type HomeserverType string

const (
	// HomeserverTuwunel uses the Matrix client-server registration endpoint.
	// If RegistrationToken is set in Config, the m.login.registration_token auth
	// flow is used.  Otherwise open (dummy) registration is attempted.
	// This is the default homeserver type.
	HomeserverTuwunel HomeserverType = "tuwunel"
	// HomeserverSynapse uses the Synapse admin shared-secret registration API.
	HomeserverSynapse HomeserverType = "synapse"
	// HomeserverGeneric uses the standard open-registration endpoint.
	HomeserverGeneric HomeserverType = "generic"
	// HomeserverManual disables automatic registration entirely.
	HomeserverManual HomeserverType = "manual"
)

// Config holds configuration for the Matrix provisioner.
type Config struct {
	// Homeserver is the Matrix homeserver URL (e.g. "https://matrix.example.com").
	Homeserver string
	// AdminUserID is the Ruriko operator's own Matrix user ID.
	// Used as the client identity when calling admin APIs.
	AdminUserID string
	// AdminAccessToken is the access token for the AdminUserID account.
	AdminAccessToken string
	// HomeserverType selects the registration strategy (default: "tuwunel").
	HomeserverType HomeserverType
	// SharedSecret is the Synapse registration_shared_secret value.
	// Required when HomeserverType == "synapse".
	SharedSecret string
	// RegistrationToken is an optional Matrix registration token used by
	// Tuwunel (and other homeservers that support m.login.registration_token).
	// When set, new accounts are registered using the token-based auth flow
	// instead of open (dummy) registration.
	RegistrationToken string
	// UsernameSuffix is an optional suffix appended to agent usernames.
	// For example, "-agent" would turn "mybot" into "mybot-agent".
	UsernameSuffix string
	// AdminRooms is a list of Matrix room IDs to invite the new agent into.
	AdminRooms []string
}

// ProvisionedAccount holds the credentials for a newly created agent account.
type ProvisionedAccount struct {
	UserID      id.UserID
	AccessToken string
}

// Provisioner manages Matrix account creation and deactivation for agents.
type Provisioner struct {
	cfg    Config
	client *mautrix.Client
	admin  *synapseadmin.Client
}

// New creates a new Provisioner.  It validates the configuration and
// initialises the underlying mautrix client.
func New(cfg Config) (*Provisioner, error) {
	if cfg.Homeserver == "" {
		return nil, fmt.Errorf("provisioning: Homeserver is required")
	}
	if cfg.AdminUserID == "" {
		return nil, fmt.Errorf("provisioning: AdminUserID is required")
	}
	if cfg.AdminAccessToken == "" {
		return nil, fmt.Errorf("provisioning: AdminAccessToken is required")
	}

	if cfg.HomeserverType == "" {
		cfg.HomeserverType = HomeserverTuwunel
	}

	if cfg.HomeserverType == HomeserverSynapse && cfg.SharedSecret == "" {
		return nil, fmt.Errorf("provisioning: SharedSecret is required for synapse homeserver type")
	}

	cli, err := mautrix.NewClient(cfg.Homeserver, id.UserID(cfg.AdminUserID), cfg.AdminAccessToken)
	if err != nil {
		return nil, fmt.Errorf("provisioning: failed to create Matrix client: %w", err)
	}

	return &Provisioner{
		cfg:    cfg,
		client: cli,
		admin:  &synapseadmin.Client{Client: cli},
	}, nil
}

// generatePassword creates a cryptographically random 32-byte hex password.
func generatePassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// validLocalpart matches the Matrix localpart character set: [a-z0-9._\-/].
var validLocalpart = regexp.MustCompile(`[^a-z0-9._\-/]`)

// usernameForAgent returns the localpart (no @, no server) for an agent.
// The agent ID is lower-cased, underscores are replaced with hyphens, and
// any characters outside the Matrix localpart set [a-z0-9._\-/] are stripped.
//
// Returns an error if sanitisation produces an empty localpart (e.g. the
// agent name contains only characters that are stripped).
func (p *Provisioner) usernameForAgent(agentID string) (string, error) {
	localpart := strings.ToLower(agentID)
	localpart = strings.ReplaceAll(localpart, "_", "-")
	localpart = validLocalpart.ReplaceAllString(localpart, "")
	if localpart == "" {
		return "", fmt.Errorf("agent name %q produces empty Matrix localpart after sanitization", agentID)
	}
	return localpart + p.cfg.UsernameSuffix, nil
}

// mxidForAgent returns the full Matrix user ID for an agent.
func (p *Provisioner) mxidForAgent(agentID string) (id.UserID, error) {
	// Extract server part from AdminUserID (@user:server → server)
	parts := strings.SplitN(string(p.cfg.AdminUserID), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid AdminUserID %q: expected @localpart:server", p.cfg.AdminUserID)
	}
	server := parts[1]
	username, err := p.usernameForAgent(agentID)
	if err != nil {
		return "", err
	}
	return id.UserID(fmt.Sprintf("@%s:%s", username, server)), nil
}

// Register creates a new Matrix account for the given agent.
// It returns the provisioned account credentials (MXID + access token).
//
// The caller is responsible for persisting the returned access token as a
// matrix_token secret before discarding it.
func (p *Provisioner) Register(ctx context.Context, agentID, displayName string) (*ProvisionedAccount, error) {
	traceID := trace.FromContext(ctx)
	password, err := generatePassword()
	if err != nil {
		return nil, err
	}

	mxid, err := p.mxidForAgent(agentID)
	if err != nil {
		return nil, err
	}

	username, err := p.usernameForAgent(agentID)
	if err != nil {
		return nil, err
	}

	slog.Info("provisioning Matrix account", "agent", agentID, "mxid", mxid, "trace", traceID)

	switch p.cfg.HomeserverType {
	case HomeserverSynapse:
		return p.registerViaSynapse(ctx, username, password, displayName, mxid)
	case HomeserverTuwunel:
		return p.registerViaTuwunel(ctx, username, password, displayName)
	case HomeserverGeneric:
		return p.registerViaClientAPI(ctx, username, password, displayName)
	default:
		return nil, fmt.Errorf("provisioning: unsupported homeserver type %q", p.cfg.HomeserverType)
	}
}

// registerViaSynapse uses the Synapse admin shared-secret API to register the
// account without requiring open registration.
func (p *Provisioner) registerViaSynapse(ctx context.Context, username, password, displayName string, expectedMXID id.UserID) (*ProvisionedAccount, error) {
	req := synapseadmin.ReqSharedSecretRegister{
		Username:    username,
		Password:    password,
		Displayname: displayName,
		UserType:    "bot",
		Admin:       false,
	}

	resp, err := p.admin.SharedSecretRegister(ctx, p.cfg.SharedSecret, req)
	if err != nil {
		return nil, fmt.Errorf("synapse registration failed for %q: %w", username, err)
	}

	slog.Info("Matrix account provisioned via Synapse admin API",
		"mxid", resp.UserID,
		"has_token", resp.AccessToken != "",
	)

	return &ProvisionedAccount{
		UserID:      resp.UserID,
		AccessToken: resp.AccessToken,
	}, nil
}

// registerViaTuwunel registers a Matrix account on a Tuwunel (or conduwuit-
// compatible) homeserver.
//
// If Config.RegistrationToken is set the m.login.registration_token auth flow
// is used, which does not require open registration.  This is the recommended
// approach: set a registration token, provision all accounts, then clear the
// token to lock down the homeserver.
//
// If no registration token is configured the m.login.dummy open-registration
// flow is used as a fallback, which requires CONDUWUIT_ALLOW_REGISTRATION=true.
func (p *Provisioner) registerViaTuwunel(ctx context.Context, username, password, displayName string) (*ProvisionedAccount, error) {
	req := &mautrix.ReqRegister{
		Username:                 username,
		Password:                 password,
		InitialDeviceDisplayName: displayName,
	}

	if p.cfg.RegistrationToken != "" {
		// Use the m.login.registration_token auth flow.
		req.Auth = struct {
			Type    string `json:"type"`
			Token   string `json:"token"`
			Session string `json:"session,omitempty"`
		}{
			Type:  "m.login.registration_token",
			Token: p.cfg.RegistrationToken,
		}
		resp, uiaResp, err := p.client.Register(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("tuwunel token registration failed for %q: %w", username, err)
		}
		_ = uiaResp // may be non-nil for multi-stage flows; we treat it as a hint only
		slog.Info("Matrix account provisioned via Tuwunel token registration",
			"mxid", resp.UserID, "username", username)
		return &ProvisionedAccount{
			UserID:      resp.UserID,
			AccessToken: resp.AccessToken,
		}, nil
	}

	// Fallback: open/dummy registration (requires CONDUWUIT_ALLOW_REGISTRATION=true).
	resp, err := p.client.RegisterDummy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tuwunel open registration failed for %q: %w (is CONDUWUIT_ALLOW_REGISTRATION=true?)", username, err)
	}
	slog.Info("Matrix account provisioned via Tuwunel open registration", "mxid", resp.UserID)
	return &ProvisionedAccount{
		UserID:      resp.UserID,
		AccessToken: resp.AccessToken,
	}, nil
}

// registerViaClientAPI uses the standard Matrix m.login.dummy open-registration
// endpoint.  Requires that open registration with the dummy auth flow is enabled
// on the homeserver.
func (p *Provisioner) registerViaClientAPI(ctx context.Context, username, password, displayName string) (*ProvisionedAccount, error) {
	req := &mautrix.ReqRegister{
		Username:                 username,
		Password:                 password,
		InitialDeviceDisplayName: displayName,
	}

	resp, err := p.client.RegisterDummy(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("client-server registration failed for %q: %w", username, err)
	}

	slog.Info("Matrix account provisioned via client-server API", "mxid", resp.UserID)

	return &ProvisionedAccount{
		UserID:      resp.UserID,
		AccessToken: resp.AccessToken,
	}, nil
}

// InviteToRooms invites the given user to all configured admin rooms so that
// the agent can join them once it has started.  Invitation errors are logged
// but do not fail the overall operation — the agent can always be re-invited
// later with `/ruriko agents matrix register`.
func (p *Provisioner) InviteToRooms(ctx context.Context, userID id.UserID) []error {
	traceID := trace.FromContext(ctx)
	var errs []error
	for _, roomID := range p.cfg.AdminRooms {
		slog.Info("inviting agent to room", "mxid", userID, "room", roomID, "trace", traceID)
		_, err := p.client.InviteUser(ctx, id.RoomID(roomID), &mautrix.ReqInviteUser{
			UserID: userID,
		})
		if err != nil {
			slog.Warn("failed to invite agent to room",
				"mxid", userID, "room", roomID, "err", err)
			errs = append(errs, fmt.Errorf("room %s: %w", roomID, err))
		}
	}
	return errs
}

// Deactivate deactivates the Matrix account for the given MXID.
// For Synapse homeservers this calls the admin deactivate endpoint.
// For other homeserver types a warning is logged and no action is taken.
//
// The erase flag requests that the homeserver purge all user data; use with care.
func (p *Provisioner) Deactivate(ctx context.Context, userID id.UserID, erase bool) error {
	traceID := trace.FromContext(ctx)
	slog.Info("deactivating Matrix account", "mxid", userID, "erase", erase, "trace", traceID)

	switch p.cfg.HomeserverType {
	case HomeserverSynapse:
		err := p.admin.DeactivateAccount(ctx, userID, synapseadmin.ReqDeleteUser{Erase: erase})
		if err != nil {
			return fmt.Errorf("failed to deactivate %s: %w", userID, err)
		}
		slog.Info("Matrix account deactivated", "mxid", userID)
		return nil
	default:
		slog.Warn("deactivation not supported for homeserver type; skipping",
			"type", p.cfg.HomeserverType, "mxid", userID)
		return nil
	}
}

// RemoveFromRooms kicks the given user from all configured admin rooms.
// Kick errors are logged but non-fatal; the caller decides whether to abort.
func (p *Provisioner) RemoveFromRooms(ctx context.Context, userID id.UserID) []error {
	traceID := trace.FromContext(ctx)
	var errs []error
	for _, roomID := range p.cfg.AdminRooms {
		slog.Info("removing agent from room", "mxid", userID, "room", roomID, "trace", traceID)
		_, err := p.client.KickUser(ctx, id.RoomID(roomID), &mautrix.ReqKickUser{
			UserID: userID,
			Reason: "Agent deprovisioned by Ruriko",
		})
		if err != nil {
			slog.Warn("failed to kick agent from room",
				"mxid", userID, "room", roomID, "err", err)
			errs = append(errs, fmt.Errorf("room %s: %w", roomID, err))
		}
	}
	return errs
}
