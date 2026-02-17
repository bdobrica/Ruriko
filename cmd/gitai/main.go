package main

import (
	"fmt"
	"os"

	"github.com/bdobrica/Ruriko/common/version"
)

func main() {
	fmt.Printf("Gitai Agent Runtime\n")
	fmt.Printf("Version: %s\n", version.Version)
	fmt.Printf("Commit: %s\n", version.GitCommit)
	fmt.Printf("Build Time: %s\n", version.BuildTime)
	fmt.Println()

	// TODO: Load Gosuto configuration
	// TODO: Connect to Matrix
	// TODO: Start Agent Control Protocol server
	// TODO: Start message processing loop
	// TODO: Initialize MCP supervisor

	fmt.Println("Gitai is not yet fully implemented.")
	fmt.Println("See TODO.md for implementation roadmap.")
	os.Exit(0)
}
