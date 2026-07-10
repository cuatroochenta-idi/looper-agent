// Command looper is the CLI tool for the Looper Agent framework.
//
// Subcommands:
//
//	serve   Start the embedded SolidJS web panel + trace ingest
//	run     Run a Go project that uses Looper Agent in debug mode
//	mcp     Start MCP debug server over stdio
//
// Usage:
//
//	looper serve [--config looper.json] [--port 9090] [--store .looper] [--db DSN]
//	looper run ./main.go --args "--input 'hello world'"
//	looper mcp
//	looper version
package main

import (
	"fmt"
	"os"
	"runtime/debug"
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
		fmt.Println("looper", versionString())
	default:
		printUsage()
		os.Exit(1)
	}
}

// versionString reports the module version embedded by `go install` /
// `go build` when the binary was built from a tagged commit. Falls back
// to "(devel)" for ad-hoc local builds — same convention as `go version
// -m`.
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi == nil {
		return "(unknown)"
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	// Local dev build — surface the VCS commit if Go embedded it.
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return "(devel " + rev + ")"
		}
	}
	return "(devel)"
}

func printUsage() {
	fmt.Println(`Looper Agent CLI

Usage:
  looper serve [flags]    Start the SolidJS web panel + trace ingest
  looper run [flags]      Run a Go project in debug mode
  looper mcp              Start MCP debug server (stdio)
  looper version          Print version

serve flags:
  --config <path>        looper.json to load (default: ./looper.json or $LOOPER_CONFIG)
  --port <n>             Web UI port (default: 9090)
  --store <dir>          Folder trace store (default: .looper)
  --db <dsn>             PostgreSQL DSN for run persistence (overrides --store)
  -- <cmd ...>           Launch a child process wired to LOOPER_TRACE_ENDPOINT

Environment:
  LOOPER_CONFIG          Path to looper.json when --config is unset
  LOOPER_DB              PostgreSQL DSN (same as --db)
  LOOPER_AUTH_PASSWORD   Enable the panel login gate (also via looper.json auth)
  LOOPER_INGEST_TOKEN    Bearer token external agents use to POST /ingest
  LOOPER_OTEL_ENABLED    Enable OpenTelemetry (default: false)
  LOOPER_OTEL_ENDPOINT   OTLP endpoint (default: localhost:4317)
  LOOPER_OTEL_INSECURE   Disable TLS (default: true)
  LOOPER_OTEL_VERBOSE    Include prompts in spans (default: false)
  LOOPER_DEBUG           Enable debug mode (default: false)`)
}
