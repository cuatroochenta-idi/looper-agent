package web

import "sync"

// Topic identifies a pub/sub channel in the Hub. The SSE endpoint maps the
// ?topics= query parameter directly onto these values, so the strings are part
// of the wire contract.
type Topic string

const (
	// TopicRuns carries list-level changes: runs_changed (coarse) and
	// run_updated (row deltas).
	TopicRuns Topic = "runs"
	// TopicChats carries chat list/thread changes: chats_changed (coarse).
	TopicChats Topic = "chats"
	// TopicSummary carries the deltas that move dashboard tiles: run_updated.
	TopicSummary Topic = "summary"
)

// TopicRun is the per-run detail topic ("run:<id>"). Subscribers receive
// step_appended, chunk (live only), and run_updated for that specific run.
func TopicRun(runID string) Topic { return Topic("run:" + runID) }

// Event is one typed message on the bus. Name is the SSE event name; Data is
// the payload, marshaled to the SSE data: line as JSON by the events endpoint.
type Event struct {
	Name string
	Data any
}

// Hub is a tiny in-memory typed pub/sub. Each subscriber registers a set of
// topics and receives every event published to ANY of them — exactly once,
// even when one Publish names several topics the subscriber holds (dedup by
// subscription identity, not by channel-per-topic).
//
// Delivery is non-blocking: a subscriber whose buffer is full drops the event.
// Every event on the wire is safe to drop — clients recover by refetching the
// REST snapshot — so a slow reader degrades to coarser updates, never a stall.
type Hub struct {
	mu   sync.Mutex
	subs map[*subscription]struct{}
}

type subscription struct {
	topics map[Topic]struct{}
	ch     chan Event
}

// NewHub returns an empty Hub ready to use.
func NewHub() *Hub { return &Hub{subs: make(map[*subscription]struct{})} }

// Subscribe registers a subscriber on the given topics and returns its event
// channel plus a cancel func that must be called when the subscriber leaves
// (typically on SSE connection close).
func (h *Hub) Subscribe(topics ...Topic) (<-chan Event, func()) {
	set := make(map[Topic]struct{}, len(topics))
	for _, t := range topics {
		set[t] = struct{}{}
	}
	sub := &subscription{topics: set, ch: make(chan Event, 64)}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		delete(h.subs, sub)
		h.mu.Unlock()
		close(sub.ch)
	}
	return sub.ch, cancel
}

// Publish delivers ev to every subscriber whose topic set intersects topics.
// Each matching subscriber receives ev at most once. Non-blocking: a full
// subscriber buffer drops the event. Holding the lock across the sends keeps
// cancel (which deletes under the same lock before closing) from ever racing a
// send onto a closed channel.
func (h *Hub) Publish(ev Event, topics ...Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if !sub.wants(topics) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

func (s *subscription) wants(topics []Topic) bool {
	for _, t := range topics {
		if _, ok := s.topics[t]; ok {
			return true
		}
	}
	return false
}
