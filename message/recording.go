package message

type appendRecording struct{ messages []Message }

// RecordAppends starts recording newly appended messages separately from the
// prompt history. Call the returned function to detach and retrieve them; it is
// idempotent. Truncate and UnmarshalJSON (used by memory compaction) do not erase
// the recording. Explicit Update/Remove edits are reflected while attached.
// The returned messages, like Messages(), are safe to read, not to mutate.
// Recording retains the uncompressed delta until stopped; it is opt-in and
// should be bounded by the caller's run limits, not used as unbounded storage.
func (h *History) RecordAppends() func() []Message {
	h.mu.Lock()
	r := &appendRecording{messages: make([]Message, 0)}
	if h.recordings == nil {
		h.recordings = make(map[*appendRecording]struct{})
	}
	h.recordings[r] = struct{}{}
	h.mu.Unlock()
	return func() []Message {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.recordings, r)
		out := make([]Message, len(r.messages))
		copy(out, r.messages)
		return out
	}
}

// appendLocked shares one write path across all public Add methods.
func (h *History) appendLocked(msg Message) {
	h.messages = append(h.messages, msg)
	for recording := range h.recordings {
		recording.messages = append(recording.messages, msg)
	}
}

func (h *History) updateRecordingsLocked(id string, updated *Message) {
	if id == "" {
		return
	}
	for recording := range h.recordings {
		for i, m := range recording.messages {
			if m.ID != id {
				continue
			}
			if updated == nil {
				recording.messages = append(recording.messages[:i], recording.messages[i+1:]...)
			} else {
				recording.messages[i] = *updated
			}
			break
		}
	}
}
