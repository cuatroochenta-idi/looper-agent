// Example 21: embed the supervision panel INSIDE your own application.
//
// Example 20 runs the panel as its own server; this one goes further for
// monolith-shaped hosts: the `analytics` package mounts the whole panel
// (SPA + JSON API + SSE + ingest + durable store) as a plain http.Handler
// under a subpath of YOUR router, protected by YOUR permission middleware.
// No sidecar process, no login gate of its own, no LOOPER_TRACE_ENDPOINT:
// agents in the same binary report through an in-process TraceSink.
//
// What it demonstrates:
//   - analytics.New with a store connector (folder here; set LOOPER_DB for
//     PostgreSQL with embedded migrations);
//   - mounting Handler() behind http.StripPrefix + an in-house auth check,
//     with BasePath telling the SPA where it lives;
//   - looper.WithTraceSink(panel): the agent's runs/steps/costs land in the
//     panel store directly — no HTTP hop;
//   - Config.IngestToken guarding the HTTP /ingest route that EXTERNAL
//     agent processes can still use.
//
// Run it:
//
//	OPENAI_API_KEY=… go run ./examples/21_embedded_analytics
//	# open http://localhost:8080/admin/looper/ (header X-Role: admin)
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/cuatroochenta-idi/looper-agent/analytics"
	"github.com/cuatroochenta-idi/looper-agent/looper"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

const panelBase = "/admin/looper"

// requireAdmin stands in for your in-house permission system: the panel is
// just another protected route of the host app.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Role") != "admin" {
			http.Error(w, "forbidden: admins only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	ctx := context.Background()

	panel, err := analytics.New(ctx, analytics.Config{
		PostgresDSN: os.Getenv("LOOPER_DB"), // empty → folder store below
		StoreDir:    ".looper",
		BasePath:    panelBase + "/",
		IngestToken: os.Getenv("LOOPER_INGEST_TOKEN"),
	})
	if err != nil {
		log.Fatalf("panel: %v", err)
	}
	defer panel.Close()

	// The host application: its own routes plus the embedded panel.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "host app — panel at "+panelBase+"/ (send X-Role: admin)")
	})
	mux.Handle(panelBase+"/", requireAdmin(http.StripPrefix(panelBase, panel.Handler())))

	// An agent in the same process, traced straight into the panel.
	agent, err := looper.NewAgent(
		openai.NewProvider(os.Getenv("OPENAI_API_KEY")),
		"You are a terse assistant.",
		looper.WithModel("gpt-5.6-luna"),
		looper.WithTraceSink(panel),
	)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}
	mux.HandleFunc("POST /ask", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		res, err := agent.Run(r.Context(), q)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, res.Output)
	})

	log.Println("host app on http://localhost:8080 — panel at " + panelBase + "/")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
