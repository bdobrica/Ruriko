// Package runtime defines shared types for the agent runtime abstraction.
package runtime

import "time"

// AgentSpec describes how an agent container should be created.
type AgentSpec struct {
	// ID is the unique agent identifier (used as container name label).
	ID string
	// DisplayName is human-readable name.
	DisplayName string
	// Image is the Docker image to use (e.g. "ghcr.io/org/gitai:v0.1.0").
	Image string
	// Template is the Gosuto template name applied to this agent.
	Template string
	// Env holds additional environment variables to inject into the container.
	Env map[string]string
	// Labels are extra Docker labels to attach to the container.
	Labels map[string]string
	// NetworkName is the Docker network to attach (defaults to "ruriko" if empty).
	NetworkName string
	// ControlPort is the HTTP port the ACP server listens on inside the container.
	ControlPort int
}

// AgentHandle identifies a running or stopped agent container.
type AgentHandle struct {
	// AgentID is the logical agent ID (matches agents.id in the DB).
	AgentID string
	// ContainerID is the Docker container ID.
	ContainerID string
	// ContainerName is the Docker container name.
	ContainerName string
	// ControlURL is the base URL for ACP calls (e.g. "http://hostname:8080").
	ControlURL string
}

// ContainerState mirrors docker container states.
type ContainerState string

const (
	StateRunning  ContainerState = "running"
	StateStopped  ContainerState = "stopped"
	StateExited   ContainerState = "exited"
	StateCreated  ContainerState = "created"
	StatePaused   ContainerState = "paused"
	StateRemoving ContainerState = "removing"
	StateUnknown  ContainerState = "unknown"
)

// RuntimeStatus holds live container status information.
type RuntimeStatus struct {
	AgentID     string
	ContainerID string
	State       ContainerState
	StartedAt   time.Time
	FinishedAt  time.Time
	ExitCode    int
	Error       string
}

// DefaultControlPort is the ACP port Gitai agents listen on.
const DefaultControlPort = 8765

// DefaultNetwork is the Docker network ruriko creates agents on.
const DefaultNetwork = "ruriko"

// ContainerNameFor returns the Docker container name for an agent ID.
func ContainerNameFor(agentID string) string {
	return "ruriko-agent-" + agentID
}
