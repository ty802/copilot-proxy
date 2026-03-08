package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// StreamTranslator reads OpenAI SSE from copilotResp and writes Anthropic SSE
// to w. It synthesises the full Anthropic event sequence:
//
//	message_start
//	  content_block_start  (index 0, type=text)
//	  ping
//	  content_block_delta* (text_delta chunks)
//	  content_block_stop
//	  [for each tool call:
//	    content_block_start  (type=tool_use)
//	    content_block_delta* (input_json_delta)
//	    content_block_stop]
//	message_delta          (stop_reason, output token count)
//	message_stop
func StreamTranslator(
	w http.ResponseWriter,
	copilotResp *http.Response,
	requestedModel string,
	inputTokenEstimate int,
) {
	flusher, canFlush := w.(http.Flusher)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Emit a helper that writes an SSE event.
	emit := func(event string, data []byte) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		if canFlush {
			flusher.Flush()
		}
	}

	// ── message_start ──────────────────────────────────────────────────────
	msgID := "msg_copilot_" + randomHex(12)
	emit("message_start", MustJSON(map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":           msgID,
			"type":         "message",
			"role":         "assistant",
			"content":      []interface{}{},
			"model":        requestedModel,
			"stop_reason":  nil,
			"stop_sequence": nil,
			"usage": map[string]int{
				"input_tokens":  inputTokenEstimate,
				"output_tokens": 1,
			},
		},
	}))

	// ── ping ────────────────────────────────────────────────────────────────
	emit("ping", MustJSON(map[string]string{"type": "ping"}))

	// State for building the Anthropic event sequence.
	type toolState struct {
		id    string
		name  string
		index int
	}

	var (
		textBlockOpen  bool
		textBlockIndex = 0
		nextBlockIndex = 0

		// tool call state: keyed by OpenAI tool call index
		toolBlocks   = map[int]*toolState{}
		outputTokens int
		stopReason   = "end_turn"
	)

	openTextBlock := func() {
		if textBlockOpen {
			return
		}
		textBlockOpen = true
		textBlockIndex = nextBlockIndex
		nextBlockIndex++
		emit("content_block_start", MustJSON(map[string]interface{}{
			"type":  "content_block_start",
			"index": textBlockIndex,
			"content_block": map[string]string{
				"type": "text",
				"text": "",
			},
		}))
	}

	closeTextBlock := func() {
		if !textBlockOpen {
			return
		}
		textBlockOpen = false
		emit("content_block_stop", MustJSON(map[string]interface{}{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		}))
	}

	// ── Read OpenAI SSE stream ───────────────────────────────────────────────
	defer copilotResp.Body.Close()
	scanner := bufio.NewScanner(copilotResp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			outputTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		// Capture finish_reason.
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			stopReason = translateFinishReason(*choice.FinishReason)
		}

		delta := choice.Delta

		// ── Text delta ─────────────────────────────────────────────────────
		if delta.Content != "" {
			openTextBlock()
			emit("content_block_delta", MustJSON(map[string]interface{}{
				"type":  "content_block_delta",
				"index": textBlockIndex,
				"delta": map[string]string{
					"type": "text_delta",
					"text": delta.Content,
				},
			}))
		}

		// ── Tool call deltas ────────────────────────────────────────────────
		for _, tc := range delta.ToolCalls {
			ts, exists := toolBlocks[tc.Index]
			if !exists {
				// New tool call — close any open text block first.
				closeTextBlock()

				blockIdx := nextBlockIndex
				nextBlockIndex++

				ts = &toolState{
					id:    tc.ID,
					name:  tc.Function.Name,
					index: blockIdx,
				}
				toolBlocks[tc.Index] = ts

				emit("content_block_start", MustJSON(map[string]interface{}{
					"type":  "content_block_start",
					"index": blockIdx,
					"content_block": map[string]interface{}{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Function.Name,
						"input": map[string]interface{}{},
					},
				}))
			}

			// Stream the JSON arguments incrementally.
			if tc.Function.Arguments != "" {
				emit("content_block_delta", MustJSON(map[string]interface{}{
					"type":  "content_block_delta",
					"index": ts.index,
					"delta": map[string]string{
						"type":         "input_json_delta",
						"partial_json": tc.Function.Arguments,
					},
				}))
			}
		}
	}

	// ── Close any open text block ────────────────────────────────────────────
	closeTextBlock()

	// ── Close all tool blocks in index order ────────────────────────────────
	// Collect and sort by Anthropic block index so events are emitted in order.
	type tsEntry struct {
		oaiIndex int
		ts       *toolState
	}
	var sorted []tsEntry
	for oaiIdx, ts := range toolBlocks {
		sorted = append(sorted, tsEntry{oaiIdx, ts})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ts.index < sorted[j].ts.index
	})
	for _, e := range sorted {
		emit("content_block_stop", MustJSON(map[string]interface{}{
			"type":  "content_block_stop",
			"index": e.ts.index,
		}))
	}

	// ── message_delta ────────────────────────────────────────────────────────
	emit("message_delta", MustJSON(map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]int{
			"output_tokens": outputTokens,
		},
	}))

	// ── message_stop ─────────────────────────────────────────────────────────
	emit("message_stop", MustJSON(map[string]string{
		"type": "message_stop",
	}))
}

// ReadNonStreamingBody reads a complete (non-streaming) JSON body from r,
// which should be an *http.Response body. Returns the parsed OpenAIResponse.
func ReadNonStreamingBody(r io.Reader) (*OpenAIResponse, error) {
	var resp OpenAIResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode OpenAI response: %w", err)
	}
	return &resp, nil
}
