package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TranslateRequest converts an Anthropic /v1/messages request into an
// OpenAI-compatible chat completions request for the GitHub Copilot API.
func TranslateRequest(req *AnthropicRequest) (*OpenAIRequest, error) {
	out := &OpenAIRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		Stop:        req.StopSeqs,
	}

	// -------------------------------------------------------------------------
	// Build messages list
	// -------------------------------------------------------------------------
	var messages []OpenAIMessage

	// Prepend system prompt if present.
	if req.System != nil {
		systemText, err := extractSystemText(req.System)
		if err != nil {
			return nil, fmt.Errorf("system field: %w", err)
		}
		if systemText != "" {
			messages = append(messages, OpenAIMessage{
				Role:    "system",
				Content: systemText,
			})
		}
	}

	// Translate each Anthropic message.
	for _, m := range req.Messages {
		oaiMsg, err := translateMessage(m)
		if err != nil {
			return nil, err
		}
		messages = append(messages, oaiMsg...)
	}

	out.Messages = messages

	// -------------------------------------------------------------------------
	// Translate tools
	// -------------------------------------------------------------------------
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	// -------------------------------------------------------------------------
	// Translate tool_choice
	// -------------------------------------------------------------------------
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "auto":
			out.ToolChoice = "auto"
		case "any":
			out.ToolChoice = "required"
		case "none":
			out.ToolChoice = "none"
		case "tool":
			out.ToolChoice = map[string]interface{}{
				"type": "function",
				"function": map[string]string{
					"name": req.ToolChoice.Name,
				},
			}
		}
	}

	return out, nil
}

// extractSystemText converts the Anthropic system field (string or content
// block array) into a plain string.
func extractSystemText(system interface{}) (string, error) {
	switch v := system.(type) {
	case string:
		return v, nil
	case []interface{}:
		// Array of content blocks — concatenate text blocks.
		var parts []string
		for _, item := range v {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n"), nil
	default:
		// Could be json.RawMessage or other — try marshal/unmarshal.
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		// Try as string first.
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, nil
		}
		// Try as array.
		var arr []map[string]interface{}
		if err := json.Unmarshal(raw, &arr); err == nil {
			var parts []string
			for _, block := range arr {
				if block["type"] == "text" {
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			return strings.Join(parts, "\n"), nil
		}
		return "", fmt.Errorf("unsupported system type: %T", v)
	}
}

// translateMessage converts a single Anthropic message (which may produce
// multiple OpenAI messages for tool_result → tool role).
func translateMessage(m AnthropicMessage) ([]OpenAIMessage, error) {
	// Content can be a plain string or an array of content blocks.
	blocks, err := normalizeContent(m.Content)
	if err != nil {
		return nil, err
	}

	switch m.Role {
	case "user":
		return translateUserMessage(blocks)
	case "assistant":
		return translateAssistantMessage(blocks)
	default:
		// Pass through unknown roles as plain text.
		text := blocksToText(blocks)
		return []OpenAIMessage{{Role: m.Role, Content: text}}, nil
	}
}

// translateUserMessage handles "user" role messages. Tool results become
// separate "tool" role messages.
func translateUserMessage(blocks []AnthropicContent) ([]OpenAIMessage, error) {
	var messages []OpenAIMessage
	var textParts []OpenAIContentPart
	var hasContent bool

	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, OpenAIContentPart{
					Type: "text",
					Text: block.Text,
				})
				hasContent = true
			}

		case "image":
			if block.Source != nil {
				var imgURL string
				switch block.Source.Type {
				case "base64":
					imgURL = fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
				case "url":
					imgURL = block.Source.URL
				}
				if imgURL != "" {
					textParts = append(textParts, OpenAIContentPart{
						Type: "image_url",
						ImageURL: &OpenAIImageURL{
							URL: imgURL,
						},
					})
					hasContent = true
				}
			}

		case "tool_result":
			// Flush any accumulated text parts first.
			if hasContent {
				msg := buildUserMessage(textParts)
				messages = append(messages, msg)
				textParts = nil
				hasContent = false
			}

			// Extract result content. The content field may be:
			//   - absent (use block.Text)
			//   - a plain string
			//   - an array of content blocks (extract text from each)
			resultContent := extractToolResultContent(block)

			messages = append(messages, OpenAIMessage{
				Role:       "tool",
				ToolCallID: block.ToolUseID,
				Content:    resultContent,
			})
		}
	}

	// Flush remaining text parts.
	if hasContent {
		messages = append(messages, buildUserMessage(textParts))
	}

	return messages, nil
}

// buildUserMessage constructs an OpenAI user message from content parts.
// If there is only one text part, uses a plain string for compatibility.
func buildUserMessage(parts []OpenAIContentPart) OpenAIMessage {
	if len(parts) == 1 && parts[0].Type == "text" {
		return OpenAIMessage{Role: "user", Content: parts[0].Text}
	}
	return OpenAIMessage{Role: "user", Content: parts}
}

// translateAssistantMessage handles "assistant" role messages. Tool uses
// become tool_calls on the assistant message.
func translateAssistantMessage(blocks []AnthropicContent) ([]OpenAIMessage, error) {
	var textParts []string
	var toolCalls []OpenAIToolCall

	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   block.ID,
				Type: "function",
				Function: OpenAIFunctionCall{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}

	msg := OpenAIMessage{Role: "assistant"}
	if len(textParts) > 0 {
		msg.Content = strings.Join(textParts, "")
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	return []OpenAIMessage{msg}, nil
}

// normalizeContent coerces the content field (string or []interface{}) into
// a slice of AnthropicContent blocks.
func normalizeContent(content interface{}) ([]AnthropicContent, error) {
	if content == nil {
		return nil, nil
	}

	switch v := content.(type) {
	case string:
		return []AnthropicContent{{Type: "text", Text: v}}, nil
	case []interface{}:
		var blocks []AnthropicContent
		for _, item := range v {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, err
			}
			var block AnthropicContent
			if err := json.Unmarshal(raw, &block); err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
		return blocks, nil
	default:
		// Try marshal round-trip.
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return []AnthropicContent{{Type: "text", Text: s}}, nil
		}
		var blocks []AnthropicContent
		if err := json.Unmarshal(raw, &blocks); err == nil {
			return blocks, nil
		}
		return nil, fmt.Errorf("unsupported content type: %T", v)
	}
}

// blocksToText concatenates the text content from a slice of content blocks.
func blocksToText(blocks []AnthropicContent) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// extractToolResultContent returns the string content to send to the OpenAI
// "tool" role message. Handles string, []AnthropicContent, and raw JSON.
func extractToolResultContent(block AnthropicContent) string {
	if block.Content != nil {
		switch v := block.Content.(type) {
		case string:
			if v != "" {
				return v
			}
		case []interface{}:
			// Array of content blocks — extract text.
			var parts []string
			for _, item := range v {
				raw, err := json.Marshal(item)
				if err != nil {
					continue
				}
				var cb AnthropicContent
				if err := json.Unmarshal(raw, &cb); err != nil {
					continue
				}
				if cb.Type == "text" && cb.Text != "" {
					parts = append(parts, cb.Text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n")
			}
		}
	}
	// Fallback to top-level text field.
	return block.Text
}
