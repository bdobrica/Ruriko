package commands

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/event"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// HandleTopologyRefresh deterministically recomputes mesh messaging targets
// from the agent's configured peer context and stores a new Gosuto version
// when the rendered topology changes.
//
// Usage: /ruriko topology refresh <agent> [--operator-room <room-id>]
func (h *Handlers) HandleTopologyRefresh(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("usage: /ruriko topology refresh <agent> [--operator-room <room-id>]")
	}

	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.refresh", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	operatorRoom := strings.TrimSpace(cmd.GetFlag("operator-room", ""))
	if operatorRoom == "" {
		operatorRoom = evt.RoomID.String()
	}
	if operatorRoom != "" && !strings.HasPrefix(operatorRoom, "!") {
		return "", fmt.Errorf("--operator-room must start with '!'")
	}

	gv, err := UpdateAgentMeshTopology(ctx, agentID, h.store, operatorRoom, evt.Sender.String())
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.refresh", agentID, "error", nil, err.Error())
		return "", err
	}

	if gv == nil {
		if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.refresh", agentID, "success",
			store.AuditPayload{"changed": false}, ""); err != nil {
			slog.Warn("audit write failed", "op", "topology.refresh", "agent", agentID, "err", err)
		}
		return fmt.Sprintf("ℹ️  Topology for **%s** is already up to date.\n\n(trace: %s)", agentID, traceID), nil
	}

	if err := h.store.PruneGosutoVersions(ctx, agentID, GosutoVersionsRetainN); err != nil {
		slog.Warn("gosuto prune failed", "agent", agentID, "err", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.refresh", agentID, "success",
		store.AuditPayload{"changed": true, "version": gv.Version, "hash": gv.Hash[:16]}, ""); err != nil {
		slog.Warn("audit write failed", "op", "topology.refresh", "agent", agentID, "err", err)
	}

	return fmt.Sprintf(
		"✅ Topology for **%s** refreshed — stored as Gosuto **v%d** (hash: `%s...`)\n\nRun `/ruriko gosuto push %s` to apply to the running agent.\n\n(trace: %s)",
		agentID, gv.Version, gv.Hash[:16], agentID, traceID,
	), nil
}

// HandleTopologyPeerSet upserts an explicit peer trust + messaging tuple into
// an agent Gosuto config. This is the deterministic widening command surface.
//
// Usage:
//
//	/ruriko topology peer-set <agent> --alias <alias> --mxid <mxid> --room <room-id> --protocol <id> [--target-room <room-id>]
func (h *Handlers) HandleTopologyPeerSet(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("usage: /ruriko topology peer-set <agent> --alias <alias> --mxid <mxid> --room <room-id> --protocol <id> [--target-room <room-id>]")
	}

	alias := strings.TrimSpace(cmd.GetFlag("alias", ""))
	mxid := strings.TrimSpace(cmd.GetFlag("mxid", ""))
	roomID := strings.TrimSpace(cmd.GetFlag("room", ""))
	protocolID := strings.TrimSpace(cmd.GetFlag("protocol", ""))
	targetRoom := strings.TrimSpace(cmd.GetFlag("target-room", roomID))

	if err := validateTopologyPeerSetFlags(alias, mxid, roomID, protocolID, targetRoom); err != nil {
		return "", err
	}

	baseGV, cfg, err := h.loadAgentGosutoForTopology(ctx, traceID, evt.Sender.String(), agentID, "topology.peer-set")
	if err != nil {
		return "", err
	}

	cfg.Trust.TrustedPeers = upsertTrustedPeer(cfg.Trust.TrustedPeers, gosutospec.TrustedPeer{
		MXID:      mxid,
		RoomID:    roomID,
		Alias:     alias,
		Protocols: []string{protocolID},
	})
	cfg.Messaging.AllowedTargets = upsertMessagingTarget(cfg.Messaging.AllowedTargets, gosutospec.MessagingTarget{
		Alias:  alias,
		RoomID: targetRoom,
	})

	rawYAML, changed, err := renderValidatedUpdatedTopology(cfg, []byte(baseGV.YAMLBlob))
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-set", agentID, "error", nil, err.Error())
		return "", err
	}
	if !changed {
		if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-set", agentID, "success",
			store.AuditPayload{"changed": false, "alias": alias, "protocol": protocolID}, ""); err != nil {
			slog.Warn("audit write failed", "op", "topology.peer-set", "agent", agentID, "err", err)
		}
		return fmt.Sprintf("ℹ️  Topology peer mapping for **%s** is unchanged.\n\n(trace: %s)", agentID, traceID), nil
	}

	// Widening operation: approval-gated.
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "topology.peer-set", agentID, cmd, evt); needed {
		return msg, err
	}

	gv, err := h.storeTopologyVersion(ctx, agentID, rawYAML, evt.Sender.String())
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-set", agentID, "error", nil, err.Error())
		return "", err
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-set", agentID, "success",
		store.AuditPayload{
			"changed":  true,
			"alias":    alias,
			"mxid":     mxid,
			"room":     roomID,
			"protocol": protocolID,
			"version":  gv.Version,
			"hash":     gv.Hash[:16],
		}, ""); err != nil {
		slog.Warn("audit write failed", "op", "topology.peer-set", "agent", agentID, "err", err)
	}

	return fmt.Sprintf(
		"✅ Topology peer mapping added for **%s** (`%s` -> `%s`) — Gosuto **v%d** (hash: `%s...`)\n\nRun `/ruriko gosuto push %s` to apply to the running agent.\n\n(trace: %s)",
		agentID, alias, protocolID, gv.Version, gv.Hash[:16], agentID, traceID,
	), nil
}

// HandleTopologyPeerRemove removes explicit peer mappings (trust and messaging)
// from an agent Gosuto config. This is a deterministic restricting operation.
//
// Usage:
//
//	/ruriko topology peer-remove <agent> --alias <alias> [--protocol <id>]
func (h *Handlers) HandleTopologyPeerRemove(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("usage: /ruriko topology peer-remove <agent> --alias <alias> [--protocol <id>]")
	}

	alias := strings.TrimSpace(cmd.GetFlag("alias", ""))
	if alias == "" {
		return "", fmt.Errorf("--alias is required")
	}
	if strings.ContainsAny(alias, " \t\r\n") {
		return "", fmt.Errorf("--alias must not contain whitespace")
	}
	protocolID := strings.TrimSpace(cmd.GetFlag("protocol", ""))

	baseGV, cfg, err := h.loadAgentGosutoForTopology(ctx, traceID, evt.Sender.String(), agentID, "topology.peer-remove")
	if err != nil {
		return "", err
	}

	cfg.Trust.TrustedPeers = removeTrustedPeer(cfg.Trust.TrustedPeers, alias, protocolID)
	if !hasTrustedPeerAlias(cfg.Trust.TrustedPeers, alias) {
		cfg.Messaging.AllowedTargets = removeMessagingTargetAlias(cfg.Messaging.AllowedTargets, alias)
	}

	rawYAML, changed, err := renderValidatedUpdatedTopology(cfg, []byte(baseGV.YAMLBlob))
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-remove", agentID, "error", nil, err.Error())
		return "", err
	}
	if !changed {
		if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-remove", agentID, "success",
			store.AuditPayload{"changed": false, "alias": alias, "protocol": protocolID}, ""); err != nil {
			slog.Warn("audit write failed", "op", "topology.peer-remove", "agent", agentID, "err", err)
		}
		return fmt.Sprintf("ℹ️  No topology changes for **%s** (alias `%s`).\n\n(trace: %s)", agentID, alias, traceID), nil
	}

	gv, err := h.storeTopologyVersion(ctx, agentID, rawYAML, evt.Sender.String())
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-remove", agentID, "error", nil, err.Error())
		return "", err
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "topology.peer-remove", agentID, "success",
		store.AuditPayload{
			"changed":  true,
			"alias":    alias,
			"protocol": protocolID,
			"version":  gv.Version,
			"hash":     gv.Hash[:16],
		}, ""); err != nil {
		slog.Warn("audit write failed", "op", "topology.peer-remove", "agent", agentID, "err", err)
	}

	return fmt.Sprintf(
		"✅ Topology peer mapping removed for **%s** (alias: `%s`) — Gosuto **v%d** (hash: `%s...`)\n\nRun `/ruriko gosuto push %s` to apply to the running agent.\n\n(trace: %s)",
		agentID, alias, gv.Version, gv.Hash[:16], agentID, traceID,
	), nil
}

func validateTopologyPeerSetFlags(alias, mxid, roomID, protocolID, targetRoom string) error {
	if alias == "" {
		return fmt.Errorf("--alias is required")
	}
	if strings.ContainsAny(alias, " \t\r\n") {
		return fmt.Errorf("--alias must not contain whitespace")
	}
	if mxid == "" || !strings.HasPrefix(mxid, "@") {
		return fmt.Errorf("--mxid must start with '@'")
	}
	if roomID == "" || !strings.HasPrefix(roomID, "!") {
		return fmt.Errorf("--room must start with '!'")
	}
	if targetRoom == "" || !strings.HasPrefix(targetRoom, "!") {
		return fmt.Errorf("--target-room must start with '!'")
	}
	if protocolID == "" {
		return fmt.Errorf("--protocol must not be empty")
	}
	return nil
}

func (h *Handlers) loadAgentGosutoForTopology(ctx context.Context, traceID, actorMXID, agentID, action string) (*store.GosutoVersion, *gosutospec.Config, error) {
	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		_ = h.store.WriteAudit(ctx, traceID, actorMXID, action, agentID, "error", nil, err.Error())
		return nil, nil, fmt.Errorf("agent not found: %s", agentID)
	}

	gv, err := h.store.GetLatestGosutoVersion(ctx, agentID)
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, actorMXID, action, agentID, "error", nil, err.Error())
		return nil, nil, fmt.Errorf("no gosuto config found for agent %q: %w", agentID, err)
	}

	var cfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(gv.YAMLBlob), &cfg); err != nil {
		return nil, nil, fmt.Errorf("invalid stored gosuto for %q: %w", agentID, err)
	}

	return gv, &cfg, nil
}

func renderValidatedUpdatedTopology(cfg *gosutospec.Config, base []byte) ([]byte, bool, error) {
	rawYAML, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("failed to serialise updated gosuto: %w", err)
	}
	if _, err := gosutospec.Parse(rawYAML); err != nil {
		return nil, false, fmt.Errorf("updated gosuto failed validation: %w", err)
	}
	if string(rawYAML) == string(base) {
		return rawYAML, false, nil
	}
	return rawYAML, true, nil
}

func (h *Handlers) storeTopologyVersion(ctx context.Context, agentID string, rawYAML []byte, actorMXID string) (*store.GosutoVersion, error) {
	sum := sha256.Sum256(rawYAML)
	hash := fmt.Sprintf("%x", sum)

	nextVer, err := h.store.NextGosutoVersion(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to determine next version: %w", err)
	}

	gv := &store.GosutoVersion{
		AgentID:       agentID,
		Version:       nextVer,
		Hash:          hash,
		YAMLBlob:      string(rawYAML),
		CreatedByMXID: actorMXID,
	}

	if err := h.store.CreateGosutoVersion(ctx, gv); err != nil {
		return nil, fmt.Errorf("failed to store gosuto version: %w", err)
	}

	if err := h.store.PruneGosutoVersions(ctx, agentID, GosutoVersionsRetainN); err != nil {
		slog.Warn("gosuto prune failed", "agent", agentID, "err", err)
	}

	return gv, nil
}

func upsertTrustedPeer(peers []gosutospec.TrustedPeer, incoming gosutospec.TrustedPeer) []gosutospec.TrustedPeer {
	for i := range peers {
		p := &peers[i]
		if p.MXID == incoming.MXID && p.RoomID == incoming.RoomID {
			if strings.TrimSpace(incoming.Alias) != "" {
				p.Alias = incoming.Alias
			}
			for _, proto := range incoming.Protocols {
				if !containsString(p.Protocols, proto) {
					p.Protocols = append(p.Protocols, proto)
				}
			}
			sort.Strings(p.Protocols)
			sortTrustedPeers(peers)
			return peers
		}
	}

	cloned := gosutospec.TrustedPeer{
		MXID:      incoming.MXID,
		RoomID:    incoming.RoomID,
		Alias:     incoming.Alias,
		Protocols: append([]string(nil), incoming.Protocols...),
	}
	sort.Strings(cloned.Protocols)
	peers = append(peers, cloned)
	sortTrustedPeers(peers)
	return peers
}

func removeTrustedPeer(peers []gosutospec.TrustedPeer, alias, protocolID string) []gosutospec.TrustedPeer {
	out := make([]gosutospec.TrustedPeer, 0, len(peers))
	for _, p := range peers {
		if p.Alias != alias {
			out = append(out, p)
			continue
		}
		if protocolID == "" {
			continue
		}

		remainingProtocols := make([]string, 0, len(p.Protocols))
		for _, proto := range p.Protocols {
			if proto != protocolID {
				remainingProtocols = append(remainingProtocols, proto)
			}
		}
		if len(remainingProtocols) == 0 {
			continue
		}
		p.Protocols = remainingProtocols
		out = append(out, p)
	}
	sortTrustedPeers(out)
	return out
}

func hasTrustedPeerAlias(peers []gosutospec.TrustedPeer, alias string) bool {
	for _, p := range peers {
		if p.Alias == alias {
			return true
		}
	}
	return false
}

func upsertMessagingTarget(targets []gosutospec.MessagingTarget, incoming gosutospec.MessagingTarget) []gosutospec.MessagingTarget {
	for i := range targets {
		if targets[i].Alias == incoming.Alias {
			targets[i].RoomID = incoming.RoomID
			sortMessagingTargets(targets)
			return targets
		}
	}
	targets = append(targets, incoming)
	sortMessagingTargets(targets)
	return targets
}

func removeMessagingTargetAlias(targets []gosutospec.MessagingTarget, alias string) []gosutospec.MessagingTarget {
	out := make([]gosutospec.MessagingTarget, 0, len(targets))
	for _, t := range targets {
		if t.Alias == alias {
			continue
		}
		out = append(out, t)
	}
	sortMessagingTargets(out)
	return out
}

func sortTrustedPeers(peers []gosutospec.TrustedPeer) {
	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].Alias != peers[j].Alias {
			return peers[i].Alias < peers[j].Alias
		}
		if peers[i].MXID != peers[j].MXID {
			return peers[i].MXID < peers[j].MXID
		}
		return peers[i].RoomID < peers[j].RoomID
	})
}

func sortMessagingTargets(targets []gosutospec.MessagingTarget) {
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Alias != targets[j].Alias {
			return targets[i].Alias < targets[j].Alias
		}
		return targets[i].RoomID < targets[j].RoomID
	})
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
