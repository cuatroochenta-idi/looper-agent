package looper

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuatroochenta-idi/looper-agent/message"
	"github.com/cuatroochenta-idi/looper-agent/provider/anthropic"
	"github.com/cuatroochenta-idi/looper-agent/provider/google"
	"github.com/cuatroochenta-idi/looper-agent/provider/openai"
)

// pngFixture is a tiny PNG header — enough to assert base64 encoding is
// stable across providers without bundling a real image.
var pngFixture = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

func multiModalHistory() []message.Message {
	return []message.Message{
		message.NewUserMessageWithParts(
			message.TextPart("describe this image:"),
			message.ImageURLPart("https://example.com/cat.png"),
		),
		message.NewUserMessageWithParts(
			message.TextPart("and compare with this one:"),
			message.ImagePart("image/png", pngFixture),
		),
	}
}

// TestMultimodal_OpenAI_EmitsContentArray asserts the OpenAI translator
// produces a JSON payload that includes the remote image URL and the inline
// base64 fixture. We don't inspect the SDK struct shape directly because
// internal field names may shift — instead we marshal-and-grep, which is
// what actually hits the wire.
func TestMultimodal_OpenAI_EmitsContentArray(t *testing.T) {
	p := openai.NewProvider("test-key-not-used")
	native := p.Translator().ToNative("you are a vision assistant", multiModalHistory(), nil)
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "https://example.com/cat.png") {
		t.Errorf("OpenAI payload missing remote URL\npayload=%s", got)
	}
	b64 := base64.StdEncoding.EncodeToString(pngFixture)
	if !strings.Contains(got, b64) {
		t.Errorf("OpenAI payload missing base64 fixture %q\npayload=%s", b64, got)
	}
	if !strings.Contains(got, "image_url") {
		t.Errorf("OpenAI payload missing image_url content type\npayload=%s", got)
	}
}

// TestMultimodal_Anthropic_EmitsImageBlock asserts the Anthropic translator
// produces a content block with type=image and surfaces both URL and base64
// inline image.
func TestMultimodal_Anthropic_EmitsImageBlock(t *testing.T) {
	p := anthropic.NewProvider("test-key-not-used")
	native := p.Translator().ToNative("you are a vision assistant", multiModalHistory(), nil)
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "https://example.com/cat.png") {
		t.Errorf("Anthropic payload missing remote URL\npayload=%s", got)
	}
	b64 := base64.StdEncoding.EncodeToString(pngFixture)
	if !strings.Contains(got, b64) {
		t.Errorf("Anthropic payload missing base64 fixture\npayload=%s", got)
	}
	if !strings.Contains(got, `"type":"image"`) {
		t.Errorf("Anthropic payload missing image block type\npayload=%s", got)
	}
}

// TestMultimodal_Google_EmitsInlineData asserts the Google translator
// produces a Part with inline data for ImagePart and a file URI / inline
// for ImageURLPart.
func TestMultimodal_Google_EmitsInlineData(t *testing.T) {
	p := google.NewProvider("test-key-not-used")
	native := p.Translator().ToNative("you are a vision assistant", multiModalHistory(), nil)
	raw, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	// Either the URL passes through directly (FileData.FileUri) or it is
	// surfaced verbatim in the part — both are acceptable on the wire.
	if !strings.Contains(got, "https://example.com/cat.png") {
		t.Errorf("Google payload missing remote URL\npayload=%s", got)
	}
	b64 := base64.StdEncoding.EncodeToString(pngFixture)
	if !strings.Contains(got, b64) {
		t.Errorf("Google payload missing base64 fixture\npayload=%s", got)
	}
}

// TestMultimodal_OpenAI_TextOnlyFastPath asserts that the pure-text legacy
// path still emits a plain string content (not an array of one TextPart),
// so we don't regress existing token-billing semantics or the wire shape
// users have been relying on.
func TestMultimodal_OpenAI_TextOnlyFastPath(t *testing.T) {
	p := openai.NewProvider("test-key-not-used")
	hist := []message.Message{message.NewUserMessage("plain text only")}
	native := p.Translator().ToNative("be brief", hist, nil)
	raw, _ := json.Marshal(native)
	got := string(raw)
	if !strings.Contains(got, `"content":"plain text only"`) {
		t.Errorf("text-only message should marshal as plain string content\npayload=%s", got)
	}
}
