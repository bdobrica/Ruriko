// Package docker provides a Docker Engine runtime adapter for spawning agent containers.
package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"

	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
)

const (
	labelManagedBy = "ruriko.managed-by"
	labelAgentID   = "ruriko.agent-id"
	labelTemplate  = "ruriko.template"
	managedByValue = "ruriko"

	// stopTimeout is how long to wait for graceful container stop before SIGKILL.
	stopTimeout = 10 * time.Second
)

// Adapter implements runtime.Runtime using the Docker Engine API.
type Adapter struct {
	client  *dockerclient.Client
	network string
}

// New creates a new Docker runtime adapter.
// Uses the DOCKER_HOST env var or the default socket path.
func New() (*Adapter, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Adapter{client: cli, network: runtime.DefaultNetwork}, nil
}

// NewWithNetwork creates an adapter using a specific Docker network name.
func NewWithNetwork(networkName string) (*Adapter, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Adapter{client: cli, network: networkName}, nil
}

// EnsureNetwork creates the ruriko Docker network if it doesn't exist.
func (a *Adapter) EnsureNetwork(ctx context.Context) error {
	nets, err := a.client.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", a.network)),
	})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range nets {
		if n.Name == a.network {
			return nil // already exists
		}
	}
	_, err = a.client.NetworkCreate(ctx, a.network, network.CreateOptions{
		Driver:     "bridge",
		Attachable: true,
		Labels:     map[string]string{labelManagedBy: managedByValue},
	})
	if err != nil {
		return fmt.Errorf("create network %q: %w", a.network, err)
	}
	return nil
}

// Spawn creates and starts an agent container from the given spec.
func (a *Adapter) Spawn(ctx context.Context, spec runtime.AgentSpec) (runtime.AgentHandle, error) {
	if spec.Image == "" {
		return runtime.AgentHandle{}, fmt.Errorf("spec.Image is required")
	}

	controlPort := spec.ControlPort
	if controlPort == 0 {
		controlPort = runtime.DefaultControlPort
	}

	networkName := spec.NetworkName
	if networkName == "" {
		networkName = a.network
	}

	containerName := runtime.ContainerNameFor(spec.ID)

	// Build environment
	env := []string{
		fmt.Sprintf("AGENT_ID=%s", spec.ID),
		fmt.Sprintf("AGENT_DISPLAY_NAME=%s", spec.DisplayName),
		fmt.Sprintf("AGENT_TEMPLATE=%s", spec.Template),
		fmt.Sprintf("ACP_PORT=%d", controlPort),
	}
	for k, v := range spec.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Build labels
	labels := map[string]string{
		labelManagedBy: managedByValue,
		labelAgentID:   spec.ID,
		labelTemplate:  spec.Template,
	}
	for k, v := range spec.Labels {
		labels[k] = v
	}

	// Container config
	containerCfg := &container.Config{
		Image:  spec.Image,
		Env:    env,
		Labels: labels,
	}

	// Host config
	hostCfg := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}

	// Network config
	networkCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}

	resp, err := a.client.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, nil, containerName)
	if err != nil {
		return runtime.AgentHandle{}, fmt.Errorf("create container: %w", err)
	}

	if err := a.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup
		_ = a.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return runtime.AgentHandle{}, fmt.Errorf("start container: %w", err)
	}

	// Inspect to get the assigned network IP for the control URL
	inspect, err := a.client.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return runtime.AgentHandle{}, fmt.Errorf("inspect container: %w", err)
	}

	controlURL := controlURLFromInspect(inspect, networkName, controlPort)

	return runtime.AgentHandle{
		AgentID:       spec.ID,
		ContainerID:   resp.ID,
		ContainerName: containerName,
		ControlURL:    controlURL,
	}, nil
}

// Stop gracefully stops the agent container.
func (a *Adapter) Stop(ctx context.Context, handle runtime.AgentHandle) error {
	timeout := int(stopTimeout.Seconds())
	if err := a.client.ContainerStop(ctx, handle.ContainerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("stop container %s: %w", handle.ContainerID, err)
	}
	return nil
}

// Start starts a previously stopped agent container without recreating it.
func (a *Adapter) Start(ctx context.Context, handle runtime.AgentHandle) error {
	if err := a.client.ContainerStart(ctx, handle.ContainerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", handle.ContainerID, err)
	}
	return nil
}

// Restart stops and starts the agent container.
func (a *Adapter) Restart(ctx context.Context, handle runtime.AgentHandle) error {
	timeout := int(stopTimeout.Seconds())
	if err := a.client.ContainerRestart(ctx, handle.ContainerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("restart container %s: %w", handle.ContainerID, err)
	}
	return nil
}

// Status returns the current runtime state of an agent container.
func (a *Adapter) Status(ctx context.Context, handle runtime.AgentHandle) (runtime.RuntimeStatus, error) {
	inspect, err := a.client.ContainerInspect(ctx, handle.ContainerID)
	if err != nil {
		if dockerclient.IsErrNotFound(err) {
			return runtime.RuntimeStatus{
				AgentID:     handle.AgentID,
				ContainerID: handle.ContainerID,
				State:       runtime.StateUnknown,
			}, nil
		}
		return runtime.RuntimeStatus{}, fmt.Errorf("inspect container: %w", err)
	}

	state := parseContainerState(inspect.State.Status)
	startedAt, _ := time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
	finishedAt, _ := time.Parse(time.RFC3339Nano, inspect.State.FinishedAt)

	return runtime.RuntimeStatus{
		AgentID:     handle.AgentID,
		ContainerID: inspect.ID,
		State:       state,
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		ExitCode:    inspect.State.ExitCode,
		Error:       inspect.State.Error,
	}, nil
}

// List returns handles for all ruriko-managed containers.
func (a *Adapter) List(ctx context.Context) ([]runtime.AgentHandle, error) {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", labelManagedBy+"="+managedByValue),
		),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	handles := make([]runtime.AgentHandle, 0, len(containers))
	for _, c := range containers {
		agentID := c.Labels[labelAgentID]
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		handles = append(handles, runtime.AgentHandle{
			AgentID:       agentID,
			ContainerID:   c.ID,
			ContainerName: name,
		})
	}
	return handles, nil
}

// Remove stops and removes the container entirely.
func (a *Adapter) Remove(ctx context.Context, handle runtime.AgentHandle) error {
	_ = a.Stop(ctx, handle) // best-effort graceful stop first
	if err := a.client.ContainerRemove(ctx, handle.ContainerID, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: false,
	}); err != nil {
		if !dockerclient.IsErrNotFound(err) {
			return fmt.Errorf("remove container: %w", err)
		}
	}
	return nil
}

// --- helpers ---

func parseContainerState(s string) runtime.ContainerState {
	switch strings.ToLower(s) {
	case "running":
		return runtime.StateRunning
	case "stopped":
		return runtime.StateStopped
	case "exited":
		return runtime.StateExited
	case "created":
		return runtime.StateCreated
	case "paused":
		return runtime.StatePaused
	case "removing":
		return runtime.StateRemoving
	default:
		return runtime.StateUnknown
	}
}

func controlURLFromInspect(inspect types.ContainerJSON, networkName string, port int) string {
	if nets := inspect.NetworkSettings.Networks; nets != nil {
		if ep, ok := nets[networkName]; ok && ep.IPAddress != "" {
			return fmt.Sprintf("http://%s:%d", ep.IPAddress, port)
		}
	}
	return fmt.Sprintf("http://localhost:%d", port)
}
