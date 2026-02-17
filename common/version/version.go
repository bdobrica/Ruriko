// Package version provides build-time version information
package version

var (
	// Version is the semantic version (set via ldflags)
	Version = "v0.0.0-dev"

	// GitCommit is the git commit hash (set via ldflags)
	GitCommit = "unknown"

	// BuildTime is the build timestamp (set via ldflags)
	BuildTime = "unknown"
)

// Info returns a formatted version string
func Info() string {
	return Version + " (" + GitCommit + ") built at " + BuildTime
}
