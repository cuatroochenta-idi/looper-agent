package web

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/starfederation/datastar-go/datastar"
)

// sseWriteTimeout bounds every individual SSE patch write. The http.Server
// deliberately runs without a global WriteTimeout (it would kill long-lived
// streams), so without a per-write deadline a half-dead client — laptop
// asleep, network dropped without RST — blocks the write forever once the
// kernel buffer fills, leaking the goroutine and pinning one of the
// browser's six per-host connections. This was the panel's hang.
const sseWriteTimeout = 15 * time.Second

// stream holds an SSE connection open and writes a fresh patch every time
// the Hub publishes on the subscribed topic. Each render is built from the
// current state, so the connection is self-healing — missed notifications
// only mean a delay, never inconsistent state.
//
// build() returns the templ component to render, plus the selector + mode
// for the datastar patch. It is called once on connect (initial render) and
// then on every notification.
func (s *Server) stream(
	w http.ResponseWriter, r *http.Request,
	topic Topic,
	selector string,
	build func() templ.Component,
) {
	sub, cancel := s.hub.Subscribe(topic)
	defer cancel()

	sse := datastar.NewSSE(w, r)
	rc := http.NewResponseController(w)

	// Initial paint.
	if err := patchInto(sse, rc, r, selector, build()); err != nil {
		logSSEError(r, err)
		return
	}

	// Heartbeat every 30 s so middleboxes don't kill an idle connection.
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-sub:
			if !ok {
				return
			}
			if err := patchInto(sse, rc, r, selector, build()); err != nil {
				logSSEError(r, err)
				return
			}
		case <-heartbeat.C:
			// Re-send current state as a keep-alive. Cheap and recovers
			// from any lost notification.
			if err := patchInto(sse, rc, r, selector, build()); err != nil {
				logSSEError(r, err)
				return
			}
		}
	}
}

// patchInto renders comp and ships it as a single datastar element patch.
// Each write is bounded by sseWriteTimeout via the ResponseController so a
// dead client errors out instead of blocking the stream goroutine forever.
func patchInto(sse *datastar.ServerSentEventGenerator, rc *http.ResponseController, r *http.Request, selector string, comp templ.Component) error {
	var buf bytes.Buffer
	if err := comp.Render(r.Context(), &buf); err != nil {
		return err
	}
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if err := sse.PatchElements(buf.String(),
		datastar.WithSelector(selector),
		datastar.WithMode(datastar.ElementPatchModeInner),
	); err != nil {
		return err
	}
	// Clear the deadline so the connection can sit idle between patches.
	return rc.SetWriteDeadline(time.Time{})
}

// logSSEError reports stream failures that are NOT the client simply going
// away. Before this, every patch error returned silently and operators had
// no signal distinguishing "tab closed" from "render panic / stuck write".
func logSSEError(r *http.Request, err error) {
	if r.Context().Err() != nil {
		return // client disconnected; expected churn
	}
	log.Printf("sse: %s %s: patch failed: %v", r.Method, r.URL.Path, err)
}

// ─── SSE handlers ────────────────────────────────────────────────────────────

func (s *Server) sseSidebar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var sig struct {
		Q        string `json:"q"`
		Status   string `json:"status"`
		Selected string `json:"selected"`
		Since    string `json:"since"`
		From     string `json:"from"`
		To       string `json:"to"`
	}
	_ = datastar.ReadSignals(r, &sig)
	// Fall back to URL query if signals aren't populated yet (first paint).
	if sig.Q == "" {
		sig.Q = q.Get("q")
	}
	if sig.Status == "" {
		sig.Status = q.Get("status")
	}
	if sig.Selected == "" {
		sig.Selected = q.Get("selected")
	}
	if sig.Since == "" {
		sig.Since = q.Get("since")
	}
	if sig.From == "" {
		sig.From = q.Get("from")
	}
	if sig.To == "" {
		sig.To = q.Get("to")
	}
	tr := TimeRange{Since: sig.Since, From: sig.From, To: sig.To}
	s.stream(w, r, TopicSidebar, "#sidebar-body", func() templ.Component {
		// Re-resolve on every push so filter/search stay live.
		return SidebarBody(s.sidebarData(sig.Status, sig.Q, sig.Selected, tr))
	})
}

func (s *Server) sseDetailPane(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.stream(w, r, TopicRun(id), "#detail-pane", func() templ.Component {
		run := s.store.Find(id)
		if run == nil {
			return emptyDetail()
		}
		return DetailPaneBody(s.detailData(run))
	})
}

func (s *Server) sseDashboard(w http.ResponseWriter, r *http.Request) {
	tr := readSignalTimeRange(r)
	s.stream(w, r, TopicSidebar, "#dashboard-body", func() templ.Component {
		return DashboardBody(s.dashboardData(tr))
	})
}

func (s *Server) sseChatSidebar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var sig struct {
		Q      string `json:"q"`
		Status string `json:"status"`
		Since  string `json:"since"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	_ = datastar.ReadSignals(r, &sig)
	if sig.Q == "" {
		sig.Q = q.Get("q")
	}
	if sig.Status == "" {
		sig.Status = q.Get("status")
	}
	if sig.Since == "" {
		sig.Since = q.Get("since")
	}
	if sig.From == "" {
		sig.From = q.Get("from")
	}
	if sig.To == "" {
		sig.To = q.Get("to")
	}
	tr := TimeRange{Since: sig.Since, From: sig.From, To: sig.To}
	s.stream(w, r, TopicChats, "#chat-sidebar-body", func() templ.Component {
		return ChatSidebarBody(s.chatSidebarData(sig.Status, sig.Q, "", tr))
	})
}

// sseChatTrace streams the chat-trace panel for a single run. Re-rendering
// the entire ChatTraceBody on every TopicRun(id) tick keeps the trace tab
// in sync with new tool calls without relying on a nested data-init inside
// the patched fragment — which used to silently fail to start, leaving the
// trace stuck on the first paint.
func (s *Server) sseChatTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.stream(w, r, TopicRun(id), "#chat-trace", func() templ.Component {
		run := s.store.Find(id)
		if run == nil {
			return emptyDetail()
		}
		return ChatTraceBody(s.detailData(run))
	})
}

func (s *Server) sseChatThread(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var sig struct {
		Q     string `json:"q"`
		Conv  string `json:"conv"`
		Since string `json:"since"`
		From  string `json:"from"`
		To    string `json:"to"`
	}
	_ = datastar.ReadSignals(r, &sig)
	if sig.Q == "" {
		sig.Q = q.Get("q")
	}
	if sig.Since == "" {
		sig.Since = q.Get("since")
	}
	if sig.From == "" {
		sig.From = q.Get("from")
	}
	if sig.To == "" {
		sig.To = q.Get("to")
	}
	tr := TimeRange{Since: sig.Since, From: sig.From, To: sig.To}
	s.stream(w, r, TopicChats, "#chat-messages", func() templ.Component {
		return chatMessagesContent(s.chatSidebarData("", sig.Q, "", tr))
	})
}
