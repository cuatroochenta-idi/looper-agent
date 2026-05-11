package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/internal/web"
)

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 9090, "Web UI port")
	otelEndpoint := fs.String("otel-endpoint", ":4317", "OTLP gRPC endpoint")
	fs.Parse(args)

	srv, err := web.NewServer()
	if err != nil {
		log.Fatalf("Failed to create web server: %v", err)
	}

	addr := fmt.Sprintf(":%d", *port)

	log.Printf("Looper Agent UI starting at http://localhost%s", addr)
	log.Printf("OTel endpoint: %s (configure via env vars)", *otelEndpoint)
	log.Printf("LOOPER_OTEL_ENDPOINT=%s", os.Getenv("LOOPER_OTEL_ENDPOINT"))

	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
