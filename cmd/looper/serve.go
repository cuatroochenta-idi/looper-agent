package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 9090, "Web UI port")
	otelEndpoint := fs.String("otel-endpoint", ":4317", "OTLP gRPC endpoint")
	fs.Parse(args)

	addr := fmt.Sprintf(":%d", *port)

	// Setup HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", dashboardHandler)
	mux.HandleFunc("/runs", runsHandler)
	mux.HandleFunc("/live", liveHandler)
	mux.HandleFunc("/api/run", apiRunHandler)

	log.Printf("Looper Agent UI starting at http://localhost%s", addr)
	log.Printf("OTel endpoint: %s (configure via env vars)", *otelEndpoint)
	log.Printf("LOOPER_OTEL_ENDPOINT=%s", os.Getenv("LOOPER_OTEL_ENDPOINT"))

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "<h1>Looper Agent Dashboard</h1><p>Coming soon...</p>")
}

func runsHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "[]")
}

func liveHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprintf(w, "data: {\"status\":\"waiting\"}\n\n")
}

func apiRunHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `{"status":"not_implemented"}`)
}
