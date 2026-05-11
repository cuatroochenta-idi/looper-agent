// Command looper is the CLI tool for the Looper Agent framework.
//
// Subcommands:
//
//	serve   Start the web UI and optional OTel collector (templ + htmx)
//	run     Run a Go project that uses Looper Agent in debug mode
//	mcp     Start MCP debug server over stdio
//
// Usage:
//
//	looper serve [--port 9090] [--otel-endpoint :4317]
//	looper run ./main.go --args "--input 'hello world'"
//	looper mcp
//	looper version
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		serveCmd(os.Args[2:])
	case "run":
		runCmd(os.Args[2:])
	case "mcp":
		mcpCmd(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("looper version 0.1.0")
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Looper Agent CLI

Usage:
  looper serve [flags]    Start web UI and OTel collector
  looper run [flags]      Run a Go project in debug mode
  looper mcp              Start MCP debug server (stdio)
  looper version          Print version

Environment:
  LOOPER_OTEL_ENABLED    Enable OpenTelemetry (default: false)
  LOOPER_OTEL_ENDPOINT   OTLP endpoint (default: localhost:4317)
  LOOPER_OTEL_INSECURE   Disable TLS (default: true)
  LOOPER_OTEL_VERBOSE    Include prompts in spans (default: false)
  LOOPER_DEBUG           Enable debug mode (default: false)`)
}
