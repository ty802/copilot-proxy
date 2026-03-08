package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	copilotChatURL = "https://api.githubcopilot.com/chat/completions"

	// Headers matching what opencode sends to the Copilot backend.
	editorVersion        = "vscode/1.107.0"
	editorPluginVersion  = "copilot-chat/0.35.0"
	copilotIntegrationID = "vscode-chat"
	proxyUserAgent       = "opencode/0.1.0"
)

// Handler is the HTTP handler for the proxy.
type Handler struct {
	mu    sync.RWMutex
	token string // GitHub OAuth token used directly as Bearer

	// refreshToken is called when a 401 is received from Copilot.
	// It should return the new token or an error.
	refreshToken func() (string, error)
}

// NewHandler creates a new proxy handler.
func NewHandler(token string, refresh func() (string, error)) *Handler {
	return &Handler{token: token, refreshToken: refresh}
}

// getToken returns the current token under a read lock.
func (h *Handler) getToken() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.token
}

// tryRefresh attempts to get a new token via refreshToken and stores it.
// Returns the new token or an error.
func (h *Handler) tryRefresh() (string, error) {
	if h.refreshToken == nil {
		return "", fmt.Errorf("no refresh function configured")
	}
	newTok, err := h.refreshToken()
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	h.token = newTok
	h.mu.Unlock()
	log.Printf("Token refreshed successfully")
	return newTok, nil
}

// modelToCopilot maps Anthropic-style model IDs (hyphens) to the Copilot API
// equivalents (dots), e.g. "claude-sonnet-4-5" → "claude-sonnet-4.5".
// If no mapping exists the name is passed through unchanged.
var modelToCopilot = map[string]string{
	"claude-haiku-4-5":  "claude-haiku-4.5",
	"claude-sonnet-4":   "claude-sonnet-4",
	"claude-sonnet-4-5": "claude-sonnet-4.5",
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	"claude-opus-4-5":   "claude-opus-4.5",
	"claude-opus-4-6":   "claude-opus-4.6",
}

// dateSuffix matches trailing date stamps Claude Code appends, e.g. -20251001
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

func copilotModel(id string) string {
	if mapped, ok := modelToCopilot[id]; ok {
		return mapped
	}
	// Strip date suffix and retry, e.g. "claude-haiku-4-5-20251001" → "claude-haiku-4-5"
	stripped := dateSuffix.ReplaceAllString(id, "")
	if stripped != id {
		if mapped, ok := modelToCopilot[stripped]; ok {
			return mapped
		}
	}
	return id
}

// ServeHTTP routes incoming requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case r.Method == http.MethodPost && path == "/v1/messages":
		h.handleMessages(w, r)
	case r.Method == http.MethodPost && path == "/v1/messages/count_tokens":
		h.handleCountTokens(w, r)
	case r.Method == http.MethodGet && (path == "/v1/models" || path == "/v1/models/"):
		h.handleModels(w, r)
	case r.Method == http.MethodGet && path == "/health":
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	case strings.HasPrefix(path, "/api/event_logging"):
		// Telemetry pings from Claude Code — silently discard.
		w.WriteHeader(http.StatusOK)
	default:
		log.Printf("404 %s %s", r.Method, path)
		writeError(w, http.StatusNotFound, "not_found_error",
			fmt.Sprintf("unknown endpoint: %s %s", r.Method, path))
	}
}

// handleMessages processes POST /v1/messages.
func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Read and parse the Anthropic request body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	defer r.Body.Close()

	var anthropicReq AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Translate to OpenAI format.
	oaiReq, err := TranslateRequest(&anthropicReq)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("request translation failed: %v", err))
		return
	}
	// Remap model name to Copilot's dot-notation.
	oaiReq.Model = copilotModel(oaiReq.Model)

	// Marshal the OpenAI request.
	oaiBody, err := json.Marshal(oaiReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error",
			fmt.Sprintf("marshal error: %v", err))
		return
	}

	doUpstream := func(tok string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, copilotChatURL, bytes.NewReader(oaiBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", proxyUserAgent)
		req.Header.Set("Editor-Version", editorVersion)
		req.Header.Set("Editor-Plugin-Version", editorPluginVersion)
		req.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
		if anthropicReq.Stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		if v := r.Header.Get("X-Request-Id"); v != "" {
			req.Header.Set("X-Request-Id", v)
		}
		return http.DefaultClient.Do(req)
	}

	resp, err := doUpstream(h.getToken())
	if err != nil {
		writeError(w, http.StatusBadGateway, "api_error",
			fmt.Sprintf("upstream request failed: %v", err))
		return
	}

	// On 401 try once to refresh the token and retry.
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		log.Printf("Got 401 from Copilot, attempting token refresh...")
		if newTok, rerr := h.tryRefresh(); rerr == nil {
			resp, err = doUpstream(newTok)
			if err != nil {
				writeError(w, http.StatusBadGateway, "api_error",
					fmt.Sprintf("upstream request failed after token refresh: %v", err))
				return
			}
		} else {
			log.Printf("Token refresh failed: %v", rerr)
			writeError(w, http.StatusUnauthorized, "authentication_error",
				"Copilot token expired and refresh failed — restart with --login")
			return
		}
	}

	// Log the round-trip.
	log.Printf("→ %s %s [model=%s stream=%v] %d %s",
		r.Method, r.URL.Path, anthropicReq.Model, anthropicReq.Stream,
		resp.StatusCode, time.Since(start).Round(time.Millisecond))

	// ── Handle upstream errors ─────────────────────────────────────────────
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		upBody, _ := io.ReadAll(resp.Body)
		log.Printf("ERROR: upstream returned %d: %s", resp.StatusCode, truncate(string(upBody), 500))

		// Map Copilot HTTP status to a sensible Anthropic error type.
		var errType string
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			errType = "authentication_error"
		case http.StatusForbidden:
			errType = "permission_error"
		case http.StatusTooManyRequests:
			errType = "rate_limit_error"
		case http.StatusBadRequest:
			errType = "invalid_request_error"
		default:
			errType = "api_error"
		}

		writeError(w, resp.StatusCode, errType,
			fmt.Sprintf("upstream error %d: %s", resp.StatusCode, truncate(string(upBody), 200)))
		return
	}

	// ── Streaming response ──────────────────────────────────────────────────
	if anthropicReq.Stream {
		// Estimate input tokens from request size (rough heuristic; Copilot
		// doesn't always return usage on streaming endpoints).
		inputEst := estimateTokens(body)
		StreamTranslator(w, resp, anthropicReq.Model, inputEst)
		return
	}

	// ── Non-streaming response ──────────────────────────────────────────────
	defer resp.Body.Close()

	oaiResp, err := ReadNonStreamingBody(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "api_error",
			fmt.Sprintf("failed to parse upstream response: %v", err))
		return
	}

	anthropicResp, err := TranslateResponse(oaiResp, anthropicReq.Model)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error",
			fmt.Sprintf("response translation failed: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(anthropicResp)
}

// handleModels returns a minimal model list so clients that probe /v1/models
// don't get a 404.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]interface{}{
		makeModel("claude-haiku-4-5"),
		makeModel("claude-sonnet-4"),
		makeModel("claude-sonnet-4-5"),
		makeModel("claude-sonnet-4-6"),
		makeModel("claude-opus-4-5"),
		makeModel("claude-opus-4-6"),
	}
	resp := map[string]interface{}{
		"object": "list",
		"data":   models,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleCountTokens handles POST /v1/messages/count_tokens.
// Copilot has no equivalent endpoint, so we estimate from request body size.
func (h *Handler) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	r.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"input_tokens": estimateTokens(body)})
}

func makeModel(id string) map[string]interface{} {
	return map[string]interface{}{
		"id":       id,
		"object":   "model",
		"created":  1700000000,
		"owned_by": "github-copilot",
	}
}

// writeError writes an Anthropic-format error response.
func writeError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(AnthropicErrorResponse(errType, message))
}

// truncate shortens a string to at most n bytes.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// estimateTokens gives a rough token count for a JSON request body.
// Anthropic counts ~4 chars per token on average.
func estimateTokens(body []byte) int {
	return len(body) / 4
}
