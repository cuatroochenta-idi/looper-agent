package message

// PartType discriminates between the kinds of payload a Part can carry.
// Each provider translator inspects the type and emits the corresponding
// native content block (OpenAI content arrays, Anthropic content blocks,
// Gemini Parts).
type PartType string

const (
	// PartText is a plain text fragment. The default and most common Part.
	PartText PartType = "text"

	// PartImageURL is a remote http(s) image — providers may fetch the URL
	// server-side or inline the bytes themselves depending on capability.
	PartImageURL PartType = "image_url"

	// PartImage is inline image bytes plus mime type. Serialized as base64
	// on the JSON wire — Go's encoding/json handles []byte → base64
	// transparently.
	PartImage PartType = "image"

	// PartFile is an inline document (PDF, CSV, etc.) with a filename hint
	// and mime type. Only providers that support document inputs accept it.
	PartFile PartType = "file"

	// PartAudio is inline audio bytes plus mime type. Used for speech /
	// transcription tasks on providers that support audio inputs.
	PartAudio PartType = "audio"
)

// Part is a single, typed piece of message content. A multi-modal message
// (e.g. text + image) is built from multiple Parts in order. Pure-text
// messages have a single PartText entry — translators use that as the
// "fast path" so the wire payload matches what providers emitted before
// multi-modal support landed.
//
// The struct is intentionally flat: a discriminated union via the Type
// field. This keeps JSON serialization simple (no custom UnmarshalJSON)
// and matches the shape each provider expects after translation.
type Part struct {
	Type PartType `json:"type"`

	// Text payload (used by PartText).
	Text string `json:"text,omitempty"`

	// URL payload (used by PartImageURL).
	URL string `json:"url,omitempty"`

	// MimeType for inline bytes (used by PartImage / PartFile / PartAudio).
	MimeType string `json:"mime_type,omitempty"`

	// Data is the raw inline payload. encoding/json base64-encodes []byte
	// automatically, so this round-trips correctly across persistence.
	Data []byte `json:"data,omitempty"`

	// Name is a filename hint (used by PartFile) so the model can reason
	// about the document by name.
	Name string `json:"name,omitempty"`
}

// TextPart builds a plain text Part. Use this in any multi-part message —
// translators concatenate adjacent text parts when emitting native content.
func TextPart(text string) Part {
	return Part{Type: PartText, Text: text}
}

// ImageURLPart builds a remote-image Part. The provider is responsible for
// fetching the URL; we only forward it.
func ImageURLPart(url string) Part {
	return Part{Type: PartImageURL, URL: url}
}

// ImagePart builds an inline image Part. Mime type is required for providers
// that need it (Anthropic, Gemini); OpenAI accepts a data: URL constructed
// from the same fields by its translator.
func ImagePart(mimeType string, data []byte) Part {
	return Part{Type: PartImage, MimeType: mimeType, Data: data}
}

// FilePart builds an inline document Part. Name is the filename hint shown
// to the model; mime type drives provider-specific content kind selection.
func FilePart(name, mimeType string, data []byte) Part {
	return Part{Type: PartFile, Name: name, MimeType: mimeType, Data: data}
}

// AudioPart builds an inline audio Part for providers that support audio
// inputs (gpt-4o-audio, gemini-2.x).
func AudioPart(mimeType string, data []byte) Part {
	return Part{Type: PartAudio, MimeType: mimeType, Data: data}
}
