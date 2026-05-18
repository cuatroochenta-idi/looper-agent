package web

import "sync"

// Topic identifies a pub/sub channel inside the Hub.
type Topic string

const (
	// TopicSidebar is the firehose subscribed by the run list. We only publish
	// on it when a card-visible field changed — run start, run finish, status
	// flip — so transient per-step churn doesn't blow away the user's clicks.
	TopicSidebar Topic = "sidebar"

	// TopicChats fires on every chat-relevant event (run_start, step,
	// run_end). The chat thread subscribes to it so streaming chunks render
	// in the agent bubble as they arrive. Kept distinct from TopicSidebar so
	// the runs sidebar doesn't get re-rendered on every token.
	TopicChats Topic = "chats"
)

// TopicRun returns the topic identifier for a specific run ID. Subscribers
// (e.g. the detail pane) see every event for that run, including each step.
func TopicRun(runID string) Topic { return Topic("run:" + runID) }

// Hub is a tiny in-memory pub/sub. Subscribers receive a notification (the
// channel just carries a token; payloads are pulled from the Store on tick).
// Slow subscribers drop notifications rather than block publishers — the
// next push will rebuild current state anyway, so loss is not catastrophic.
//
// Publishers are responsible for naming every topic they care about; the Hub
// does not fan out implicitly. Step-level publishers send only TopicRun(id);
// structural events (run_start / run_end) also send TopicSidebar.
type Hub struct {
	mu   sync.Mutex
	subs map[Topic]map[chan struct{}]bool
}

// NewHub returns an empty Hub ready to use.
func NewHub() *Hub {
	return &Hub{subs: make(map[Topic]map[chan struct{}]bool)}
}

// Subscribe registers a notification channel on the given topic. The returned
// cancel func must be called when the subscriber goes away (typically on
// SSE connection close).
func (h *Hub) Subscribe(t Topic) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 4)
	h.mu.Lock()
	if h.subs[t] == nil {
		h.subs[t] = make(map[chan struct{}]bool)
	}
	h.subs[t][ch] = true
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if m := h.subs[t]; m != nil {
			delete(m, ch)
			if len(m) == 0 {
				delete(h.subs, t)
			}
		}
		h.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// Publish wakes every subscriber on the given topic. Non-blocking: if a
// subscriber's buffer is full it skips that one.
func (h *Hub) Publish(topics ...Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range topics {
		for ch := range h.subs[t] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}
