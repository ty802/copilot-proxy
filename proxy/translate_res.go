package proxy

import (
	"encoding/json"
	"fmt"
)

// TranslateResponse converts a non-streaming OpenAI response into the
// Anthropic /v1/messages response format.
func TranslateResponse(oai *OpenAIResponse, requestedModel string) (*AnthropicResponse, error) {
	resp := &AnthropicResponse{
		ID:    oai.ID,
		Type:  "message",
		Role:  "assistant",
		Model: requestedModel,
		Usage: AnthropicUsage{
			InputTokens:  oai.Usage.PromptTokens,
			OutputTokens: oai.Usage.CompletionTokens,
		},
	}

	if len(oai.Choices) == 0 {
		resp.StopReason = "end_turn"
		return resp, nil
	}

	choice := oai.Choices[0]
	resp.StopReason = translateFinishReason(choice.FinishReason)

	// Translate message content.
	content, err := translateChoiceMessage(choice.Message)
	if err != nil {
		return nil, err
	}
	resp.Content = content

	return resp, nil
}

// translateChoiceMessage converts an OpenAI assistant message to Anthropic
// content blocks.
func translateChoiceMessage(msg OpenAIMessage) ([]AnthropicContent, error) {
	var blocks []AnthropicContent

	// Plain text content.
	switch v := msg.Content.(type) {
	case string:
		if v != "" {
			blocks = append(blocks, AnthropicContent{
				Type: "text",
				Text: v,
			})
		}
	case []interface{}:
		for _, item := range v {
			raw, _ := json.Marshal(item)
			var part OpenAIContentPart
			if err := json.Unmarshal(raw, &part); err == nil && part.Type == "text" {
				blocks = append(blocks, AnthropicContent{
					Type: "text",
					Text: part.Text,
				})
			}
		}
	}

	// Tool calls.
	for _, tc := range msg.ToolCalls {
		var input json.RawMessage
		if tc.Function.Arguments != "" {
			input = json.RawMessage(tc.Function.Arguments)
		} else {
			input = json.RawMessage("{}")
		}

		// Validate it's valid JSON; wrap in object if not.
		var check interface{}
		if err := json.Unmarshal(input, &check); err != nil {
			input = json.RawMessage("{}")
		}

		blocks = append(blocks, AnthropicContent{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	return blocks, nil
}

// translateFinishReason maps OpenAI finish_reason to Anthropic stop_reason.
func translateFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	default:
		if reason == "" {
			return "end_turn"
		}
		return "end_turn"
	}
}

// AnthropicErrorResponse builds a well-formed Anthropic error JSON body.
func AnthropicErrorResponse(errType, message string) []byte {
	resp := map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// MustJSON marshals v to JSON, panicking on error (for use in tests / SSE).
func MustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("MustJSON: %v", err))
	}
	return b
}
