package web

import (
	"fmt"
	"time"
)

// Persistence is the durable backing store for run snapshots. The in-memory
// Store stays the hot cache serving every read; Persistence is the shared
// source of truth that lets other panel replicas hydrate runs (including ones
// still running) and lets a restart recover history. A nil Persistence means
// the panel runs in-memory only — runs vanish on exit.
//
// Backends live outside this package (e.g. internal/store/postgres) and import
// web for RunRecord; web never imports them, so the seam stays cycle-free.
type Persistence interface {
	// SaveRun upserts a run snapshot — on every meaningful ingest event
	// (write-through, so other replicas can hydrate a run while it is still
	// running) and when the sweeper finalizes a stuck run. Must be idempotent
	// on RunRecord.ID.
	SaveRun(r *RunRecord) error
	// LoadRuns returns every persisted run in chronological (started_at) order.
	LoadRuns() ([]*RunRecord, error)
	// LoadRunsSince returns the runs whose LastSeenAt is at or after since, in
	// chronological (started_at) order — the incremental read behind the
	// cross-replica hydrator.
	LoadRunsSince(since time.Time) ([]*RunRecord, error)
	// Close releases any backend resources (pools, handles).
	Close() error
}

// PersistableSnapshot returns a denoised copy of r suitable for durable
// storage: live-only streaming_chunk / reasoning_chunk steps are dropped so a
// reload is byte-identical regardless of backend, matching what the folder
// backend has always written to disk. Backends should persist this shape.
func PersistableSnapshot(r *RunRecord) *RunRecord {
	c := r.Clone()
	c.Steps = stripChunkSteps(r.Steps)
	return c
}

// folderPersistence stores one JSON file per run under a directory — the
// original .looper/ behavior, now behind the Persistence seam.
type folderPersistence struct{ dir string }

// NewFolderPersistence prepares dir (created if missing, added to .gitignore)
// and returns a directory-backed Persistence. An empty dir is a usage error;
// callers wanting in-memory-only should pass a nil Persistence instead.
func NewFolderPersistence(dir string) (Persistence, error) {
	if dir == "" {
		return nil, fmt.Errorf("folder persistence: empty directory (use nil Persistence for in-memory only)")
	}
	if err := ensureStoreDir(dir); err != nil {
		return nil, err
	}
	return &folderPersistence{dir: dir}, nil
}

func (f *folderPersistence) SaveRun(r *RunRecord) error { return writeRunFile(f.dir, r) }

func (f *folderPersistence) LoadRuns() ([]*RunRecord, error) { return loadRunsFromDisk(f.dir) }

func (f *folderPersistence) LoadRunsSince(since time.Time) ([]*RunRecord, error) {
	all, err := loadRunsFromDisk(f.dir)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if !r.LastSeenAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *folderPersistence) Close() error { return nil }
