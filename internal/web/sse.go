package web

import (
	"bytes"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/starfederation/datastar-go/datastar"
)

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

	// Initial paint.
	if err := patchInto(sse, r, selector, build()); err != nil {
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
			if err := patchInto(sse, r, selector, build()); err != nil {
				return
			}
		case <-heartbeat.C:
			// Re-send current state as a keep-alive. Cheap and recovers
			// from any lost notification.
			if err := patchInto(sse, r, selector, build()); err != nil {
				return
			}
		}
	}
}

// patchInto renders comp and ships it as a single datastar element patch.
func patchInto(sse *datastar.ServerSentEventGenerator, r *http.Request, selector string, comp templ.Component) error {
	var buf bytes.Buffer
	if err := comp.Render(r.Context(), &buf); err != nil {
		return err
	}
	return sse.PatchElements(buf.String(),
		datastar.WithSelector(selector),
		datastar.WithMode(datastar.ElementPatchModeInner),
	)
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
