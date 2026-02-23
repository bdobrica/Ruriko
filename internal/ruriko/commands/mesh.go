package commands

// mesh.go implements the R15.4 mesh topology computation for the provisioning
// pipeline. When Ruriko provisions an agent whose Gosuto defines peer agents
// (in instructions.context.peers), this module resolves each peer's admin room
// ID from the Ruriko agent inventory and injects the resulting
// messaging.allowedTargets into the rendered Gosuto YAML.
//
// Design:
//
//   - Peer admin rooms are resolved from the peer agent's latest stored Gosuto
//     config (trust.adminRoom field). This allows per-agent room assignments.
//   - A "user" target is always added when an operator room ID is provided, so
//     the agent can reply directly to the human operator.
//   - If a peer agent is not yet provisioned (no Gosuto stored), it is skipped
//     with a warning log. The mesh can be re-computed later via gosuto push.
//   - The topology is versioned with the Gosuto: changing peers or re-provisioning
//     produces a new Gosuto version with updated messaging targets.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
	"gopkg.in/yaml.v3"
)

// meshStore defines the subset of store.Store used by the mesh topology
// resolver so it can be mocked in tests.
type meshStore interface {
	GetAgent(ctx context.Context, id string) (*store.Agent, error)
	GetLatestGosutoVersion(ctx context.Context, agentID string) (*store.GosutoVersion, error)
}

// ResolveMeshTopology computes the messaging.allowedTargets for an agent based
// on its Gosuto peer list and the current agent inventory.
//
// For each peer listed in cfg.Instructions.Context.Peers, the function looks up
// the peer agent's latest Gosuto config and extracts trust.adminRoom as the
// target room ID. If the peer has no stored Gosuto config (not yet provisioned),
// it is skipped and a warning is logged.
//
// If operatorRoomID is non-empty, a "user" target is appended so the agent can
// message the human operator directly.
//
// The returned slice is ready to be assigned to cfg.Messaging.AllowedTargets.
func ResolveMeshTopology(
	ctx context.Context,
	cfg *gosutospec.Config,
	s meshStore,
	operatorRoomID string,
) []gosutospec.MessagingTarget {
	var targets []gosutospec.MessagingTarget

	for _, peer := range cfg.Instructions.Context.Peers {
		roomID, err := resolvePeerAdminRoom(ctx, s, peer.Name)
		if err != nil {
			slog.Warn("mesh: could not resolve peer admin room; skipping",
				"peer", peer.Name, "agent", cfg.Metadata.Name, "err", err)
			continue
		}
		targets = append(targets, gosutospec.MessagingTarget{
			RoomID: roomID,
			Alias:  peer.Name,
		})
	}

	// Always add the "user" target so agents can report to the operator.
	if operatorRoomID != "" {
		targets = append(targets, gosutospec.MessagingTarget{
			RoomID: operatorRoomID,
			Alias:  "user",
		})
	}

	return targets
}

// resolvePeerAdminRoom looks up the given peer agent's admin room from its
// latest stored Gosuto config. Returns the trust.adminRoom value or an error
// if the agent or its config cannot be found.
func resolvePeerAdminRoom(ctx context.Context, s meshStore, peerName string) (string, error) {
	// Verify the peer agent exists in the inventory.
	if _, err := s.GetAgent(ctx, peerName); err != nil {
		return "", fmt.Errorf("peer agent %q not in inventory: %w", peerName, err)
	}

	gv, err := s.GetLatestGosutoVersion(ctx, peerName)
	if err != nil {
		return "", fmt.Errorf("peer agent %q has no gosuto config: %w", peerName, err)
	}

	var peerCfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(gv.YAMLBlob), &peerCfg); err != nil {
		return "", fmt.Errorf("failed to parse peer %q gosuto yaml: %w", peerName, err)
	}

	if peerCfg.Trust.AdminRoom == "" {
		return "", fmt.Errorf("peer agent %q gosuto has no trust.adminRoom set", peerName)
	}

	return peerCfg.Trust.AdminRoom, nil
}

// InjectMeshTopology parses the rendered Gosuto YAML, computes the mesh
// topology from the agent's peer list, injects the messaging.allowedTargets,
// and re-serialises the config back to YAML.
//
// If the config has no peers defined and no operatorRoomID is provided, the
// YAML is returned unchanged (no messaging section injected).
//
// Any existing messaging.allowedTargets in the config are replaced; the
// maxMessagesPerMinute value is preserved if already set.
func InjectMeshTopology(
	ctx context.Context,
	renderedYAML []byte,
	s meshStore,
	operatorRoomID string,
) ([]byte, error) {
	var cfg gosutospec.Config
	if err := yaml.Unmarshal(renderedYAML, &cfg); err != nil {
		return nil, fmt.Errorf("mesh: failed to parse gosuto yaml: %w", err)
	}

	// Only inject mesh topology for configs that use the standard Gosuto schema.
	// Non-standard configs (e.g. legacy or test templates) are returned unchanged.
	if cfg.APIVersion != gosutospec.SpecVersion {
		return renderedYAML, nil
	}

	targets := ResolveMeshTopology(ctx, &cfg, s, operatorRoomID)

	// Nothing to inject — return unchanged.
	if len(targets) == 0 {
		return renderedYAML, nil
	}

	cfg.Messaging.AllowedTargets = targets

	// Apply a sensible default rate limit if none was set.
	if cfg.Messaging.MaxMessagesPerMinute == 0 {
		cfg.Messaging.MaxMessagesPerMinute = 30
	}

	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("mesh: failed to serialise gosuto yaml: %w", err)
	}

	return out, nil
}

// UpdateAgentMeshTopology re-computes and injects the mesh topology for an
// existing agent. It loads the agent's latest Gosuto version, resolves the
// mesh, and stores the updated config as a new Gosuto version.
//
// This is used when the mesh topology needs to be refreshed — for example,
// after a new peer agent is provisioned and its admin room becomes known.
//
// Returns the new GosutoVersion, or nil if no changes were needed or possible.
func UpdateAgentMeshTopology(
	ctx context.Context,
	agentID string,
	s *store.Store,
	operatorRoomID string,
	operatorMXID string,
) (*store.GosutoVersion, error) {
	gv, err := s.GetLatestGosutoVersion(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("mesh update: no gosuto for agent %q: %w", agentID, err)
	}

	updated, err := InjectMeshTopology(ctx, []byte(gv.YAMLBlob), s, operatorRoomID)
	if err != nil {
		return nil, fmt.Errorf("mesh update: inject failed for agent %q: %w", agentID, err)
	}

	// If the YAML didn't change, skip creating a new version.
	if string(updated) == gv.YAMLBlob {
		return nil, nil
	}

	nextVer, err := s.NextGosutoVersion(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("mesh update: next version: %w", err)
	}

	newGV := &store.GosutoVersion{
		AgentID:       agentID,
		Version:       nextVer,
		Hash:          hashYAML(updated),
		YAMLBlob:      string(updated),
		CreatedByMXID: operatorMXID,
	}
	if err := s.CreateGosutoVersion(ctx, newGV); err != nil {
		return nil, fmt.Errorf("mesh update: store version: %w", err)
	}

	slog.Info("mesh: updated messaging topology",
		"agent", agentID,
		"version", newGV.Version,
		"hash", newGV.Hash[:8])

	return newGV, nil
}

// hashYAML returns the SHA-256 hex hash of a YAML byte slice.
func hashYAML(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}
