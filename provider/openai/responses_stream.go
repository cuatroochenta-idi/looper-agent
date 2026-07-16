// Streaming over the Responses API.
//
// The stream contract replicates ChatStream's (chat/completions) exactly:
// a synchronous first-event probe so pre-content HTTP failures surface as
// the function-return error (the silent-failover guard), a buffered
// channel with per-chunk provenance stamping, and a final chunk carrying
// cumulative content, tool calls, and usage.
package openai

import (
	"context"
	"fmt"

	"github.com/cuatroochenta-idi/looper-agent/provider"

	"github.com/openai/openai-go/responses"
)

// Stream event type discriminators handled by chatStreamResponses. The
// SDK's ResponseStreamEventUnion switches on this string; every event not
// listed here is deliberately ignored — the response.completed event
// carries the full final Response, so item-level added/done events don't
// need to be accumulated (simpler and atomic).
const (
	eventTypeOutputTextDelta           = "response.output_text.delta"
	eventTypeReasoningSummaryTextDelta = "response.reasoning_summary_text.delta"
	eventTypeResponseCompleted         = "response.completed"
	eventTypeResponseFailed            = "response.failed"
	eventTypeResponseIncomplete        = "response.incomplete"
	eventTypeError                     = "error"
)

// chatStreamResponses is the streaming /v1/responses counterpart of
// ChatStream.
func (p *Provider) chatStreamResponses(ctx context.Context, req provider.LLMRequest) (<-chan provider.StreamChunk, error) {
	params, model, err := p.buildResponsesParams(req)
	if err != nil {
		return nil, err
	}

	includeReasoning := p.shouldIncludeReasoning(req.Reasoning)
	stream := p.client.Responses.NewStreaming(ctx, params)

	// Synchronous first-event probe — same rationale as ChatStream's
	// first-chunk probe (see openai.go): the SDK defers the HTTP request
	// to the first Next() call, so without this a pre-content failure
	// (400 invalid request, 401 auth, 404 no such model) would be buried
	// in the final chunk's Error after we already returned (channel, nil),
	// and FailoverProvider / RetryProvider would stay committed to the
	// broken inner. Probing shifts no latency: the loop blocks on the
	// first chunk anyway, and Next() respects ctx cancellation.
	var pendingEvent *responses.ResponseStreamEventUnion
	if stream.Next() {
		e := stream.Current()
		pendingEvent = &e
	} else if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("openai responses stream: %w", err)
	}

	ch := make(chan provider.StreamChunk, 64)
	// Precomputed once per stream — every chunk emission stamps it so the
	// trace UI can attribute per-chunk without re-deriving the key surface.
	keySuffix := provider.APIKeySuffix(p.apiKeys[0])

	go func() {
		defer close(ch)
		var contentBuilder string
		// finalResp is the full Response embedded in response.completed —
		// the authoritative source for tool calls and usage.
		var finalResp *responses.Response
		// failErr / failUsage capture terminal failure events
		// (response.failed / response.incomplete / error). Usage seen on a
		// failure is still billed — same rule as the chat path.
		var failErr error
		var failUsage *provider.Usage

		// Per-event handler, factored as a closure so the probed first
		// event and the rest of the stream go through the same branches.
		handleEvent := func(ev responses.ResponseStreamEventUnion) {
			switch ev.Type {
			case eventTypeOutputTextDelta:
				if d := ev.Delta.OfString; d != "" {
					contentBuilder += d
					ch <- provider.StreamChunk{Content: d, ProviderID: p.providerID, ModelID: model, APIKeySuffix: keySuffix}
				}
			case eventTypeReasoningSummaryTextDelta:
				if d := ev.Delta.OfString; d != "" && includeReasoning {
					ch <- provider.StreamChunk{Reasoning: d, ProviderID: p.providerID, ModelID: model, APIKeySuffix: keySuffix}
				}
			case eventTypeResponseCompleted:
				r := ev.Response
				finalResp = &r
			case eventTypeResponseFailed, eventTypeResponseIncomplete:
				r := ev.Response
				msg := r.Error.Message
				if msg == "" && r.IncompleteDetails.Reason != "" {
					msg = string(r.IncompleteDetails.Reason)
				}
				if msg == "" {
					msg = ev.Type
				}
				failErr = fmt.Errorf("openai responses stream: %s: %s", ev.Type, msg)
				if r.Usage.TotalTokens > 0 || r.Usage.InputTokens > 0 {
					u := usageFromResponses(r.Usage)
					failUsage = &u
				}
			case eventTypeError:
				failErr = fmt.Errorf("openai responses stream: error event: %s (code %s)", ev.Message, ev.Code)
			}
		}

		if pendingEvent != nil {
			handleEvent(*pendingEvent)
		}
		for stream.Next() {
			handleEvent(stream.Current())
		}

		// Deltas are the incremental source of truth for content (same as
		// the chat path's contentBuilder); the completed Response supplies
		// tool calls (with the signature blob) and usage.
		final := provider.StreamChunk{Content: contentBuilder, IsFinal: true}
		if finalResp != nil {
			out := walkResponsesOutput(finalResp, includeReasoning)
			final.ToolCalls = out.ToolCalls
			// Defensive: if the server emitted no text deltas but the
			// completed output carries text, don't lose the reply.
			if final.Content == "" {
				final.Content = out.Content
			}
			u := usageFromResponses(finalResp.Usage)
			final.Usage = &u
		}
		if failErr != nil {
			final.Error = failErr
			if failUsage != nil {
				final.Usage = failUsage
			}
		}
		// Surface stream errors (HTTP failures mid-stream, malformed SSE,
		// connection drops, server "error" frames). Pre-content failures
		// were already handled by the synchronous probe above. No vLLM
		// SSE-comment tolerance here: this path only ever talks to the
		// real api.openai.com.
		if err := stream.Err(); err != nil {
			final.Error = fmt.Errorf("openai responses stream: %w", err)
		}
		// Provenance lives on the final chunk too — same place Usage is
		// set, which is where the loop attributes cost.
		final.ProviderID = p.providerID
		final.ModelID = model
		final.APIKeySuffix = keySuffix
		ch <- final
	}()

	return ch, nil
}
