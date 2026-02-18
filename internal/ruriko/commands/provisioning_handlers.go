package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// HandleAgentsMatrixRegister provisions (or associates) a Matrix account for an
// existing agent and stores the access token as a secret.
//
// Usage:
//
//	/ruriko agents matrix register <name>            - auto-provision via homeserver admin API
//	/ruriko agents matrix register <name> --mxid <@user:example.com>  - use existing account
//
// # Flags
//
//   - --mxid <@user:example.com>  — associate an already-existing Matrix account
//     instead of creating a new one.  The access token for that account must be
//     stored separately as a secret named "agent.<name>.matrix_token".
//   - --invite-rooms true|false   — whether to invite the agent to configured
//     admin rooms (default: true).
func (h *Handlers) HandleAgentsMatrixRegister(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	// Command structure: /ruriko agents matrix register <name>
	// Router: name="agents", subcommand="matrix", args[0]="register", args[1]=<name>
	subOp, _ := cmd.GetArg(0)
	if subOp != "register" {
		return "", fmt.Errorf("usage: /ruriko agents matrix register <name> [--mxid <@user:server>]")
	}

	agentID, ok := cmd.GetArg(1)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko agents matrix register <name> [--mxid <@user:server>]")
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.matrix.register", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	// Determine the MXID to use.
	existingMXID := cmd.GetFlag("mxid", "")

	if existingMXID != "" {
		// --mxid flag: associate pre-existing account without calling the admin API.
		if err := h.store.UpdateAgentMXID(ctx, agentID, existingMXID); err != nil {
			h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.matrix.register", agentID, "error", nil, err.Error())
			return "", fmt.Errorf("failed to update agent MXID: %w", err)
		}

		slog.Info("associated pre-existing Matrix account", "agent", agentID, "mxid", existingMXID)

		if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.matrix.register", agentID, "success",
			store.AuditPayload{"mxid": existingMXID, "provisioned": false}, ""); err != nil {
			slog.Warn("audit write failed", "op", "agents.matrix.register", "err", err)
		}

		return fmt.Sprintf(`✅ Agent **%s** associated with Matrix account

MXID: %s

⚠️  The access token for this account must be stored manually:
/ruriko secrets set agent.%s.matrix_token --type matrix_token --value <base64-token>
Then bind it:
/ruriko secrets bind %s agent.%s.matrix_token

(trace: %s)`, agentID, existingMXID, agentID, agentID, agentID, traceID), nil
	}

	// Auto-provision via admin API.
	if h.provisioner == nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.matrix.register", agentID, "error", nil, "no provisioner configured")
		return "", fmt.Errorf("Matrix provisioning is not configured (MATRIX_HOMESERVER_TYPE, MATRIX_SHARED_SECRET etc. must be set)")
	}

	if agent.MXID.Valid && agent.MXID.String != "" {
		return fmt.Sprintf("⚠️  Agent **%s** already has a Matrix account: %s\n\nUse --mxid to reassign.\n\n(trace: %s)",
			agentID, agent.MXID.String, traceID), nil
	}

	provisioned, err := h.provisioner.Register(ctx, agentID, agent.DisplayName)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.matrix.register", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("Matrix account provisioning failed: %w", err)
	}

	// Persist MXID in agents table.
	if err := h.store.UpdateAgentMXID(ctx, agentID, provisioned.UserID.String()); err != nil {
		// Non-fatal: the account exists, we just failed to record it.  Log loudly.
		slog.Error("failed to persist agent MXID after provisioning",
			"agent", agentID, "mxid", provisioned.UserID, "err", err)
	}

	// Store access token as a matrix_token secret.
	secretName := fmt.Sprintf("agent.%s.matrix_token", agentID)
	tokenBytes := []byte(provisioned.AccessToken)
	if err := h.secrets.Set(ctx, secretName, secrets.TypeMatrixToken, tokenBytes); err != nil {
		// If we can't store the token, that is serious: log it but tell the user.
		slog.Error("failed to store agent access token as secret",
			"agent", agentID, "secret", secretName, "err", err)
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.matrix.register", agentID, "error", nil,
			fmt.Sprintf("account created but token storage failed: %v", err))
		return "", fmt.Errorf("Matrix account created (%s) but access token could not be stored: %w", provisioned.UserID, err)
	}

	// Auto-bind the secret to the agent.
	if err := h.secrets.Bind(ctx, agentID, secretName, "matrix_identity"); err != nil {
		slog.Warn("failed to auto-bind matrix_token secret",
			"agent", agentID, "secret", secretName, "err", err)
	}

	// Invite to configured admin rooms (non-fatal).
	inviteErrs := h.provisioner.InviteToRooms(ctx, provisioned.UserID)
	var inviteNote string
	if len(inviteErrs) > 0 {
		msgs := make([]string, len(inviteErrs))
		for i, e := range inviteErrs {
			msgs[i] = e.Error()
		}
		inviteNote = fmt.Sprintf("\n⚠️  Room invite errors (non-fatal):\n%s", strings.Join(msgs, "\n"))
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.matrix.register", agentID, "success",
		store.AuditPayload{"mxid": provisioned.UserID.String(), "provisioned": true, "secret": secretName}, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.matrix.register", "err", err)
	}

	return fmt.Sprintf(`✅ Matrix account provisioned for agent **%s**

MXID:        %s
Secret:      %s (auto-bound)%s

(trace: %s)`,
		agentID,
		provisioned.UserID,
		secretName,
		inviteNote,
		traceID,
	), nil
}

// HandleAgentsDisable soft-disables an agent:
//   - Stops the container (if running)
//   - Kicks the agent from configured rooms
//   - Deactivates the Matrix account (Synapse only)
//   - Marks the agent as "disabled" in the database
//
// Usage: /ruriko agents disable <name> [--erase]
//
// Flags:
//   - --erase  — request Synapse to erase all user data (irreversible)
func (h *Handlers) HandleAgentsDisable(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko agents disable <name> [--erase]")
	}

	erase := cmd.HasFlag("erase")

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.disable", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	if agent.Status == "disabled" {
		return fmt.Sprintf("⚠️  Agent **%s** is already disabled\n\n(trace: %s)", agentID, traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔒 Disabling agent **%s**...\n\n", agentID))

	// Step 1: Stop container if running.
	if h.runtime != nil && agent.ContainerID.Valid {
		handle := runtime.AgentHandle{
			AgentID:     agentID,
			ContainerID: agent.ContainerID.String,
		}
		if stopErr := h.runtime.Stop(ctx, handle); stopErr != nil {
			slog.Warn("disable: failed to stop container", "agent", agentID, "err", stopErr)
			sb.WriteString(fmt.Sprintf("⚠️  Container stop failed (continuing): %v\n", stopErr))
		} else {
			sb.WriteString("✅ Container stopped\n")
		}
	}

	// Step 2: Matrix deprovisioning (kick from rooms + deactivate account).
	if h.provisioner != nil && agent.MXID.Valid && agent.MXID.String != "" {
		mxid := id.UserID(agent.MXID.String)

		// Remove from rooms.
		kickErrs := h.provisioner.RemoveFromRooms(ctx, mxid)
		if len(kickErrs) > 0 {
			for _, e := range kickErrs {
				slog.Warn("disable: room kick error", "agent", agentID, "err", e)
				sb.WriteString(fmt.Sprintf("⚠️  Room kick error (continuing): %v\n", e))
			}
		} else {
			sb.WriteString("✅ Removed from rooms\n")
		}

		// Deactivate account.
		if deactivateErr := h.provisioner.Deactivate(ctx, mxid, erase); deactivateErr != nil {
			slog.Warn("disable: account deactivation failed", "agent", agentID, "mxid", mxid, "err", deactivateErr)
			sb.WriteString(fmt.Sprintf("⚠️  Account deactivation failed (continuing): %v\n", deactivateErr))
		} else {
			if erase {
				sb.WriteString(fmt.Sprintf("✅ Matrix account %s deactivated and data erased\n", mxid))
			} else {
				sb.WriteString(fmt.Sprintf("✅ Matrix account %s deactivated\n", mxid))
			}
		}
	} else if agent.MXID.Valid && agent.MXID.String != "" {
		sb.WriteString(fmt.Sprintf("⚠️  Provisioner not configured — Matrix account %s was NOT deactivated\n", agent.MXID.String))
	}

	// Step 3: Mark disabled in database.
	if err := h.store.UpdateAgentDisabled(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.disable", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to mark agent as disabled: %w", err)
	}
	sb.WriteString("✅ Agent marked as disabled\n")

	// Step 4: Delete stored matrix_token secret (the access token is now invalid).
	secretName := fmt.Sprintf("agent.%s.matrix_token", agentID)
	if delErr := h.secrets.Delete(ctx, secretName); delErr != nil {
		// Secret may not exist — that's fine.
		slog.Debug("disable: matrix token secret not found or already removed",
			"agent", agentID, "secret", secretName, "err", delErr)
	} else {
		sb.WriteString(fmt.Sprintf("✅ Secret %s removed\n", secretName))
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.disable", agentID, "success",
		store.AuditPayload{"mxid": agent.MXID.String, "erase": erase}, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.disable", "err", err)
	}

	sb.WriteString(fmt.Sprintf("\n(trace: %s)", traceID))
	return sb.String(), nil
}
