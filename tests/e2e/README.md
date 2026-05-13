# Nautilus E2E suite

These tests pin the wire-shape contract of every major framework feature
against the real OpenAI / Gemini / Anthropic APIs. They are **gated by a
build tag** so the default `go test ./...` ignores them — running them
costs real tokens and requires API keys.

## Running

```sh
# Load credentials (or export OPENAI_API_KEY / GOOGLE_API_KEY / GEMINI_API_KEY)
set -a && source .env.local && set +a

# Run the whole e2e suite
go test -tags e2e ./tests/e2e/... -v

# Run a single area
go test -tags e2e ./tests/e2e/... -run TestE2E_Multimodal -v
```

Each test calls `t.Skip` when its required env var is missing, so missing
keys produce SKIPs rather than failures.

## What's covered

| File | Feature | Provider(s) |
|------|---------|-------------|
| `multimodal_test.go` | Text + image via `NewUserMessageWithParts` | OpenAI, Gemini |
| `structured_output_test.go` | `WithStructuredOutput[T]` + `Decode[T]` (native + tool-fallback paths) | OpenAI, Gemini, Anthropic |
| `tool_calling_test.go` | Basic tool call with real LLM | OpenAI |
| `validator_retry_test.go` | `TurnValidator` rejection re-prompt | OpenAI |

## Why a build tag, not just env-var skip

Build-tag gating avoids importing the suite at all in normal builds — so a
broken e2e test (e.g. SDK version skew) doesn't break `go test`,
`go build`, or `go vet`. Maintainers opt in explicitly when running the
real-network suite.
