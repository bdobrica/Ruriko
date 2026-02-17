// Package runtime defines the Runtime interface for agent container lifecycle management.
package runtime

import "context"

// Runtime abstracts the container orchestration backend (Docker, local, k8s, etc.)
type Runtime interface {
	// Spawn creates and starts a new agent container from the given spec.
	// Returns a handle identifying the running container.
	Spawn(ctx context.Context, spec AgentSpec) (AgentHandle, error)

	// Stop gracefully stops the agent container.
	Stop(ctx context.Context, handle AgentHandle) error

	// Start starts a previously stopped agent container without recreating it.
	Start(ctx context.Context, handle AgentHandle) error

	// Restart stops and then starts the agent container.
	Restart(ctx context.Context, handle AgentHandle) error

	// Status returns the current runtime status of an agent container.
	Status(ctx context.Context, handle AgentHandle) (RuntimeStatus, error)

	// List returns handles for all containers managed by this runtime.
	// Only containers with the ruriko label are returned.
	List(ctx context.Context) ([]AgentHandle, error)

	// Remove stops and deletes the container. Use before removing an agent from the DB.
	Remove(ctx context.Context, handle AgentHandle) error
}
