package message

import (
	"encoding/json"
	"testing"
)

// TestPart_TextConstructor asserts the most common case round-trips: a single
// text Part keeps its content intact and the part type is "text".
func TestPart_TextConstructor(t *testing.T) {
	p := TextPart("hello world")
	if p.Type != PartText {
		t.Errorf("expected PartText, got %q", p.Type)
	}
	if p.Text != "hello world" {
		t.Errorf("expected text payload, got %q", p.Text)
	}
}

// TestPart_ImageURLConstructor asserts remote-image parts capture only URL +
// optional mime hint, never inline bytes.
func TestPart_ImageURLConstructor(t *testing.T) {
	p := ImageURLPart("https://example.com/cat.png")
	if p.Type != PartImageURL {
		t.Errorf("expected PartImageURL, got %q", p.Type)
	}
	if p.URL != "https://example.com/cat.png" {
		t.Errorf("expected URL preserved, got %q", p.URL)
	}
	if len(p.Data) != 0 {
		t.Errorf("URL parts must not carry inline data, got %d bytes", len(p.Data))
	}
}

// TestPart_ImageConstructor pins the inline-bytes shape: mime type required,
// data preserved verbatim.
func TestPart_ImageConstructor(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4e, 0x47} // PNG magic header
	p := ImagePart("image/png", data)
	if p.Type != PartImage {
		t.Errorf("expected PartImage, got %q", p.Type)
	}
	if p.MimeType != "image/png" {
		t.Errorf("expected mime preserved, got %q", p.MimeType)
	}
	if string(p.Data) != string(data) {
		t.Errorf("expected data preserved")
	}
}

// TestPart_FileConstructor asserts files carry a filename hint plus bytes.
func TestPart_FileConstructor(t *testing.T) {
	p := FilePart("report.pdf", "application/pdf", []byte("%PDF-"))
	if p.Type != PartFile {
		t.Errorf("expected PartFile, got %q", p.Type)
	}
	if p.Name != "report.pdf" {
		t.Errorf("expected filename preserved, got %q", p.Name)
	}
}

// TestPart_JSONRoundTrip asserts a part can be persisted via JSON and read
// back identical — required for History.MarshalJSON used by resume flows.
func TestPart_JSONRoundTrip(t *testing.T) {
	in := ImagePart("image/png", []byte("abc"))
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Part
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Type != in.Type || out.MimeType != in.MimeType || string(out.Data) != string(in.Data) {
		t.Errorf("round-trip lost data: in=%+v out=%+v", in, out)
	}
}

// TestMessage_NewUserMessage_BackwardCompat asserts that legacy callers using
// NewUserMessage(string) still get a usable Message AND that Parts is
// populated with a single TextPart so translators can read Parts uniformly.
func TestMessage_NewUserMessage_BackwardCompat(t *testing.T) {
	m := NewUserMessage("hello")
	if m.Content != "hello" {
		t.Errorf("Content should remain populated for old code, got %q", m.Content)
	}
	if len(m.Parts) != 1 {
		t.Fatalf("expected 1 Part synthesized from Content, got %d", len(m.Parts))
	}
	if m.Parts[0].Type != PartText || m.Parts[0].Text != "hello" {
		t.Errorf("Part should mirror Content, got %+v", m.Parts[0])
	}
}

// TestMessage_NewUserMessageWithParts_PopulatesContentFromText asserts the
// reverse direction: a multi-part message exposes the concatenated text via
// Content so legacy consumers that read m.Content for logging / hashing still
// see the textual portion.
func TestMessage_NewUserMessageWithParts_PopulatesContentFromText(t *testing.T) {
	m := NewUserMessageWithParts(
		TextPart("look at this:"),
		ImageURLPart("https://example.com/cat.png"),
		TextPart("what do you see?"),
	)
	if len(m.Parts) != 3 {
		t.Fatalf("expected 3 Parts, got %d", len(m.Parts))
	}
	want := "look at this:\nwhat do you see?"
	if m.Content != want {
		t.Errorf("Content should concat text parts with newlines, got %q want %q", m.Content, want)
	}
}

// TestHistory_AddUserMessageParts asserts the History helper preserves Parts
// when round-tripped through MarshalJSON / UnmarshalJSON — the persistence
// path used by Pause/Resume.
func TestHistory_AddUserMessageParts(t *testing.T) {
	h := NewHistory()
	h.AddUserMessageParts(TextPart("question"), ImageURLPart("https://x/y.png"))

	raw, err := h.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored, err := UnmarshalHistory(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := restored.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Parts) != 2 {
		t.Fatalf("expected 2 Parts after restore, got %d", len(msgs[0].Parts))
	}
	if msgs[0].Parts[1].URL != "https://x/y.png" {
		t.Errorf("URL Part not restored, got %+v", msgs[0].Parts[1])
	}
}
