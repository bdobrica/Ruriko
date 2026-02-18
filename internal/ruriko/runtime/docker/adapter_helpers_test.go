package docker

// adapter_helpers_test.go — unit tests for pure helper functions (issue #20).
//
// These tests cover the two helper functions that previously had no test coverage:
//   - parseContainerState: maps Docker status strings → runtime.ContainerState
//   - controlURLFromInspect: extracts the ACP control URL from a container inspect result

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"

	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
)

// --- parseContainerState ---------------------------------------------------

func TestParseContainerState(t *testing.T) {
	cases := []struct {
		input string
		want  runtime.ContainerState
	}{
		{"running", runtime.StateRunning},
		{"RUNNING", runtime.StateRunning}, // case-insensitive
		{"stopped", runtime.StateStopped},
		{"exited", runtime.StateExited},
		{"created", runtime.StateCreated},
		{"paused", runtime.StatePaused},
		{"removing", runtime.StateRemoving},
		{"dead", runtime.StateUnknown},
		{"", runtime.StateUnknown},
		{"restarting", runtime.StateUnknown},
	}

	for _, tc := range cases {
		got := parseContainerState(tc.input)
		if got != tc.want {
			t.Errorf("parseContainerState(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- controlURLFromInspect -------------------------------------------------

// buildInspect is a helper that constructs a minimal ContainerJSON with the
// specified network/IP mapping.
func buildInspect(networkName, ipAddress string) types.ContainerJSON {
	nets := map[string]*network.EndpointSettings{}
	if networkName != "" {
		nets[networkName] = &network.EndpointSettings{IPAddress: ipAddress}
	}
	return types.ContainerJSON{
		NetworkSettings: &types.NetworkSettings{
			Networks: nets,
		},
	}
}

func TestControlURLFromInspect_WithNetworkIP(t *testing.T) {
	inspect := buildInspect("ruriko", "172.20.0.5")
	got := controlURLFromInspect(inspect, "ruriko", 8080)
	want := "http://172.20.0.5:8080"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestControlURLFromInspect_DifferentPort(t *testing.T) {
	inspect := buildInspect("mynet", "10.0.0.2")
	got := controlURLFromInspect(inspect, "mynet", 9999)
	want := "http://10.0.0.2:9999"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestControlURLFromInspect_EmptyIP_FallsBackToLocalhost(t *testing.T) {
	// Network entry present but IP not yet assigned.
	inspect := buildInspect("ruriko", "")
	got := controlURLFromInspect(inspect, "ruriko", 8080)
	want := "http://localhost:8080"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestControlURLFromInspect_NetworkNotFound_FallsBackToLocalhost(t *testing.T) {
	// Container result contains a different network, not the one we query.
	inspect := buildInspect("other-network", "192.168.1.1")
	got := controlURLFromInspect(inspect, "ruriko", 8080)
	want := "http://localhost:8080"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestControlURLFromInspect_NilNetworks_FallsBackToLocalhost(t *testing.T) {
	inspect := types.ContainerJSON{
		NetworkSettings: &types.NetworkSettings{
			Networks: nil,
		},
	}
	got := controlURLFromInspect(inspect, "ruriko", 8080)
	want := "http://localhost:8080"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
