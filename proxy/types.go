// Package proxy contains the HTTP handler and format translation logic.
package proxy

import "encoding/json"

// =============================================================================
// Anthropic API types (what Claude Code sends / expects)
// =============================================================================

// AnthropicRequest is the body of POST /v1/messages.
type AnthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []AnthropicMessage `json:"messages"`
	System      interface{}        `json:"system,omitempty"` // string or []AnthropicContent
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	TopK        *int               `json:"top_k,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	StopSeqs    []string           `json:"stop_sequences,omitempty"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	ToolChoice  *AnthropicToolChoice `json:"tool_choice,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// AnthropicMessage is a single turn in the conversation.
type AnthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []AnthropicContent
}

// AnthropicContent is a structured content block.
type AnthropicContent struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	// For tool_use blocks (assistant → client):
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	// For tool_result blocks (user → assistant):
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	// For tool_result, content can be a nested array of blocks.
	Content    interface{}     `json:"content,omitempty"` // string or []AnthropicContent
	// For image blocks:
	Source     *AnthropicImageSource `json:"source,omitempty"`
}

// AnthropicImageSource describes an image in a content block.
type AnthropicImageSource struct {
	Type      string `json:"type"`       // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// AnthropicTool describes a function the model can call.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// AnthropicToolChoice controls how the model selects tools.
type AnthropicToolChoice struct {
	Type string `json:"type"` // "auto", "any", "tool", "none"
	Name string `json:"name,omitempty"`
}

// AnthropicResponse is the non-streaming response body.
type AnthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []AnthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        AnthropicUsage     `json:"usage"`
}

// AnthropicUsage tracks token counts in the Anthropic format.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicError is the error envelope Anthropic returns on failure.
type AnthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// =============================================================================
// OpenAI / Copilot API types (what we send / receive from GitHub Copilot)
// =============================================================================

// OpenAIRequest is the body for POST /chat/completions.
type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
	ToolChoice  interface{}     `json:"tool_choice,omitempty"` // "auto","none","required" or object
}

// OpenAIMessage is a single chat message.
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content,omitempty"` // string or []OpenAIContentPart
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// OpenAIContentPart is a typed content piece (text or image_url).
type OpenAIContentPart struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL *OpenAIImageURL   `json:"image_url,omitempty"`
}

// OpenAIImageURL holds a URL or base64 data URI for an image.
type OpenAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// OpenAITool is a function definition in OpenAI format.
type OpenAITool struct {
	Type     string           `json:"type"` // always "function"
	Function OpenAIFunction   `json:"function"`
}

// OpenAIFunction is the actual function schema.
type OpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// OpenAIToolCall is a tool invocation in an assistant message.
type OpenAIToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"` // "function"
	Function OpenAIFunctionCall  `json:"function"`
}

// OpenAIFunctionCall holds the function name and JSON arguments.
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// OpenAIResponse is the non-streaming response body.
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage"`
}

// OpenAIChoice is one completion candidate.
type OpenAIChoice struct {
	Index        int            `json:"index"`
	Message      OpenAIMessage  `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

// OpenAIUsage tracks token counts in the OpenAI format.
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIStreamChunk is a single SSE data payload for streaming.
type OpenAIStreamChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Model   string              `json:"model"`
	Choices []OpenAIStreamChoice `json:"choices"`
	Usage   *OpenAIUsage         `json:"usage,omitempty"`
}

// OpenAIStreamChoice is a streaming completion delta.
type OpenAIStreamChoice struct {
	Index        int              `json:"index"`
	Delta        OpenAIStreamDelta `json:"delta"`
	FinishReason *string          `json:"finish_reason"`
}

// OpenAIStreamDelta is the incremental content in a streaming chunk.
type OpenAIStreamDelta struct {
	Role      string                   `json:"role,omitempty"`
	Content   string                   `json:"content,omitempty"`
	ToolCalls []OpenAIStreamToolCall   `json:"tool_calls,omitempty"`
}

// OpenAIStreamToolCall is a tool call delta inside a streaming chunk.
// It has an Index field that identifies which tool call is being updated.
type OpenAIStreamToolCall struct {
	Index    int                        `json:"index"`
	ID       string                     `json:"id,omitempty"`
	Type     string                     `json:"type,omitempty"` // "function"
	Function OpenAIStreamFunctionDelta  `json:"function"`
}

// OpenAIStreamFunctionDelta carries incremental function name/arguments.
type OpenAIStreamFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}
