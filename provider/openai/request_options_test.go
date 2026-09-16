package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// A client option handed through WithRequestOptions reaches the SDK: the
// provider's requests travel through the supplied http.Client, which is
// what a per-attempt logging transport needs.
func TestWithRequestOptions_ReachesTheSDKClient(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	seen := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen++
		return http.DefaultTransport.RoundTrip(r)
	})}

	p := NewProvider("k",
		WithModel("m"),
		WithBaseURL(srv.URL),
		WithRequestOptions(option.WithHTTPClient(client), option.WithMaxRetries(0)),
	)
	if _, err := p.client.Chat.Completions.New(t.Context(), openai.ChatCompletionNewParams{
		Model:    "m",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	}); err != nil {
		t.Fatalf("request through the supplied client failed: %v", err)
	}
	if seen != 1 || hits != 1 {
		t.Fatalf("supplied transport saw %d request(s), server %d; want 1 and 1", seen, hits)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
