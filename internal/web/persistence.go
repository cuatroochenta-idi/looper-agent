package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─── Run files ────────────────────────────────────────────────────────────────
//
// Each completed run is written to <storeDir>/<RFC3339-date>_<short-id>.json.
// The leading timestamp makes filesystem sort match chronological order, so
// hydration replays runs in start-order without any extra bookkeeping.

const gitignoreLine = "# Looper Agent trace store — agent runs streamed by LOOPER_TRACE_ENDPOINT.\n.looper/\n"

// ensureStoreDir creates the trace directory if missing and adds it to the
// project's .gitignore (the parent of storeDir, typically the CWD).
func ensureStoreDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return ensureGitignored(dir)
}

// ensureGitignored appends the store directory to a .gitignore in its parent
// if not already present. Best-effort: doesn't fail if .gitignore is missing
// or in a non-git directory.
func ensureGitignored(dir string) error {
	parent := filepath.Dir(dir)
	if parent == "." || parent == "" {
		parent = "."
	}
	gi := filepath.Join(parent, ".gitignore")

	body, err := os.ReadFile(gi)
	if err != nil && !os.IsNotExist(err) {
		return nil // silent: not our place to refuse if read failed
	}
	base := filepath.Base(dir) + "/"
	if hasGitignoreEntry(string(body), base) {
		return nil
	}
	// Open append, create if missing. Best-effort: failure is benign.
	f, err := os.OpenFile(gi, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	defer f.Close()
	// Ensure preceding newline if the file doesn't end with one.
	if len(body) > 0 && body[len(body)-1] != '\n' {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(gitignoreLine)
	return nil
}

func hasGitignoreEntry(content, entry string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == entry || line == strings.TrimSuffix(entry, "/") {
			return true
		}
	}
	return false
}

// runFileName builds the on-disk filename for a finalized run.
//
//	2026-05-12T10-15-30.123_8f95d4a2.json
func runFileName(startedAt time.Time, id string) string {
	ts := startedAt.UTC().Format("2006-01-02T15-04-05.000")
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s_%s.json", ts, short)
}

// writeRunFile serializes a single run snapshot to disk. Atomic via tmp+rename.
//
// streaming_chunk and reasoning_chunk steps are stripped from the persisted
// step list: a single turn produces dozens-to-hundreds of deltas which bloat
// the JSON, dominate diffs, and add no information that StepLLMResponse
// (one event per turn, with the accumulated assistant text) doesn't already
// carry. The in-memory store keeps them for the live stream — only the
// disk snapshot is denoised.
func writeRunFile(dir string, r *RunRecord) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := runFileName(r.StartedAt, r.ID)
	final := filepath.Join(dir, name)
	tmp := final + ".tmp"

	snapshot := *r
	snapshot.Steps = stripChunkSteps(r.Steps)

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&snapshot); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// stripChunkSteps returns a copy of steps with every streaming_chunk and
// reasoning_chunk dropped. Returns the input slice as-is when no chunks
// are present (cheap path: in-process runs with mid-stream deltas hit the
// allocation, ingest-only runs already arrive denoised and skip it).
func stripChunkSteps(steps []TimelineStep) []TimelineStep {
	keep := 0
	for _, s := range steps {
		if s.Kind == StepKindStreamingChunk || s.Kind == StepKindReasoning {
			continue
		}
		keep++
	}
	if keep == len(steps) {
		return steps
	}
	out := make([]TimelineStep, 0, keep)
	for _, s := range steps {
		if s.Kind == StepKindStreamingChunk || s.Kind == StepKindReasoning {
			continue
		}
		out = append(out, s)
	}
	return out
}

// loadRunsFromDisk hydrates every run found in dir into store. Returns the
// number of records loaded. Bad files are logged and skipped.
func loadRunsFromDisk(dir string, store *Store) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	type loaded struct {
		name string
		run  *RunRecord
	}
	all := make([]loaded, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var r RunRecord
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		if r.ID == "" {
			continue
		}
		all = append(all, loaded{name: e.Name(), run: &r})
	}
	// Sort by filename (timestamp prefix) so insertion is chronological.
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	for _, x := range all {
		store.Add(x.run)
	}
	return len(all), nil
}
