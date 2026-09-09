package message

import (
	"testing"
)

func TestRecordAppendsSurvivesCompactionAndDoesNotReplayPrefix(t *testing.T) {
	h := NewHistory()
	h.AddUserMessage("old")
	finish := h.RecordAppends()
	h.AddUserMessage("new")
	h.AddAssistantMessage("before compaction", nil)
	h.Truncate(1)
	if err := h.UnmarshalJSON([]byte(`[{"id":"summary","type":"system","content":"summary"}]`)); err != nil {
		t.Fatal(err)
	}
	h.AddAssistantMessage("after compaction", nil)
	got := finish()
	if len(got) != 3 || got[0].Content != "new" || got[1].Content != "before compaction" || got[2].Content != "after compaction" {
		t.Fatalf("recorded=%+v", got)
	}
	h.AddSystemMessage("later")
	if len(finish()) != 3 {
		t.Fatal("finished recording kept growing")
	}
}

func TestRecordAppendsHonorsExplicitUpdateAndRemove(t *testing.T) {
	h := NewHistory()
	finish := h.RecordAppends()
	h.AddAssistantMessage("private", nil)
	h.Update(0, func(m *Message) { m.Content = "redacted" })
	h.AddAssistantMessage("remove", nil)
	h.Remove(1)
	got := finish()
	if len(got) != 1 || got[0].Content != "redacted" {
		t.Fatalf("recorded=%+v", got)
	}
}

func TestRecordingPartsSurviveHistoryReplacement(t *testing.T) {
	h := NewHistory()
	finish := h.RecordAppends()
	h.AddUserMessage("original request")
	compacted := NewHistory()
	compacted.AddSystemMessage("summary")
	raw, err := compacted.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	got := finish()
	if len(got) != 1 || got[0].Content != "original request" || got[0].Parts[0].Text != "original request" {
		t.Fatalf("recording corrupted by compaction: %+v", got)
	}
}
