package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuatroochenta-idi/looper-agent/looper"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// The in-process sink must land runs in the panel store without HTTP.
func TestPanelSinkFeedsAPI(t *testing.T) {
	p, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	now := time.Now()
	p.TraceEvent(looper.TraceEvent{
		Type: looper.TraceRunStart, RunID: "run-embedded-1", Ts: now,
		Data: mustJSON(t, looper.RunStartData{Input: "hola", StartedAt: now.Format(time.RFC3339Nano)}),
	})
	p.TraceEvent(looper.TraceEvent{
		Type: looper.TraceRunEnd, RunID: "run-embedded-1", Ts: now.Add(time.Second),
		Data: mustJSON(t, looper.RunEndData{
			Status: "completed", Turns: 1, TotalUSD: 0.5, CostEstimated: true,
			EndedAt: now.Add(time.Second).Format(time.RFC3339Nano),
		}),
	})

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state/runs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/state/runs = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "run-embedded-1") {
		t.Fatalf("runs list missing sink-fed run: %s", body)
	}
}

// The SPA probes /api/me; embedded panels must answer JSON (open panel),
// never the SPA fallback page.
func TestPanelMeProbeIsOpen(t *testing.T) {
	p, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/me", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/me = %d, want 200", rec.Code)
	}
	var me struct {
		AuthEnabled   bool `json:"auth_enabled"`
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("/api/me is not JSON: %v (%s)", err, rec.Body.String())
	}
	if me.AuthEnabled || !me.Authenticated {
		t.Fatalf("/api/me = %+v, want open panel", me)
	}
}

// Mounted under a subpath, the index page must carry the injected base so
// the SPA aims assets, API calls, and routes at the mount point.
func TestPanelBasePathInjection(t *testing.T) {
	p, err := New(context.Background(), Config{BasePath: "/admin/looper/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	host := http.NewServeMux()
	host.Handle("/admin/looper/", http.StripPrefix("/admin/looper", p.Handler()))

	for _, path := range []string{"/admin/looper/", "/admin/looper/runs/some-run"} {
		rec := httptest.NewRecorder()
		host.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `window.__LOOPER_BASE__="/admin/looper/"`) {
			t.Fatalf("GET %s: missing __LOOPER_BASE__ injection: %s", path, body)
		}
		if !strings.Contains(body, `<base href="/admin/looper/">`) {
			t.Fatalf("GET %s: missing <base> tag: %s", path, body)
		}
	}
}

// When an ingest token is configured, POST /ingest requires the bearer and
// every other route stays untouched.
func TestPanelIngestToken(t *testing.T) {
	p, err := New(context.Background(), Config{IngestToken: "s3cret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	ev := `{"type":"run_start","run_id":"r-ext","data":{"input":"x","started_at":"2026-07-10T10:00:00Z"}}`

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/ingest", strings.NewReader(ev)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /ingest without bearer = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(ev))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /ingest with bearer = %d, want 204", rec.Code)
	}

	rec = httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state/summary", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/state/summary = %d, want 200 (token must only guard /ingest)", rec.Code)
	}
}
