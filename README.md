### Note this project is Vibcoded!!!!

# copilot-proxy

**TL;DR:** A local proxy that translates Anthropic API requests to GitHub Copilot API calls, letting you use Claude models in Claude Code powered by your GitHub Copilot subscription.

## What is this?

This proxy sits between Claude Code and GitHub Copilot, translating Anthropic's message format to OpenAI's format (which Copilot uses). It authenticates via GitHub OAuth and forwards requests to `api.githubcopilot.com`, allowing you to access Claude models (Haiku, Sonnet, Opus) through your existing Copilot subscription.

## Features

- Translates Anthropic API → GitHub Copilot API in real-time
- Handles both streaming and non-streaming responses
- Automatic GitHub OAuth device flow authentication
- Token refresh on expiration
- Model name translation (e.g., `claude-sonnet-4-5` → `claude-sonnet-4.5`)
- Compatible with Claude Code CLI

## Prerequisites

- GitHub account with Copilot subscription
- Go 1.22 or later
- Claude Code CLI installed

## Installation

```bash
git clone https://github.com/ty802/copilot-proxy
cd copilot-proxy
go build
```

## Usage

1. **Start the proxy:**
   ```bash
   ./copilot-proxy
   ```

   On first run, you'll be prompted to authenticate with GitHub:
   ```
   GitHub Authentication Required
   ──────────────────────────────────────────
   1. Open:  https://github.com/login/device
   2. Enter: XXXX-XXXX
   ──────────────────────────────────────────
   ```

2. **Configure Claude Code:**
   ```bash
   export ANTHROPIC_BASE_URL=http://localhost:8080
   export ANTHROPIC_API_KEY=dummy
   ```

3. **Run Claude Code:**
   ```bash
   claude
   ```

### Command-line options

```bash
./copilot-proxy --port 8080      # Custom port (default: 8080)
./copilot-proxy --login          # Force new GitHub authentication
```

## How it works

1. **Authentication**: Uses GitHub OAuth device flow (same as opencode) to obtain a token
2. **Request Translation**: Converts Anthropic's Messages API format to OpenAI's Chat Completions format
3. **Forwarding**: Sends translated requests to `api.githubcopilot.com`
4. **Response Translation**: Converts OpenAI responses back to Anthropic format
5. **Streaming**: Handles Server-Sent Events (SSE) for streaming responses

## Supported Models

- `claude-haiku-4-5` → `claude-haiku-4.5`
- `claude-sonnet-4` → `claude-sonnet-4`
- `claude-sonnet-4-5` → `claude-sonnet-4.5`
- `claude-sonnet-4-6` → `claude-sonnet-4.6`
- `claude-opus-4-5` → `claude-opus-4.5`
- `claude-opus-4-6` → `claude-opus-4.6`

## Architecture

```
Claude Code CLI
    ↓ (Anthropic API format)
copilot-proxy (localhost:8080)
    ↓ (OpenAI API format)
api.githubcopilot.com
    ↓ (Claude models via Copilot)
Response translated back
```

## Files

- `main.go` - Entry point and server setup
- `auth/auth.go` - GitHub OAuth device flow and token management
- `proxy/handler.go` - HTTP routing and request/response handling
- `proxy/translate_req.go` - Anthropic → OpenAI request translation
- `proxy/translate_res.go` - OpenAI → Anthropic response translation
- `proxy/stream.go` - SSE streaming response handler
- `proxy/types.go` - Type definitions for both API formats

## Token Storage

Tokens are stored in `~/.local/share/opencode/auth.json` (compatible with opencode). The proxy will reuse existing tokens if available.

## Troubleshooting

**401 Unauthorized:**
- Run `./copilot-proxy --login` to re-authenticate
- Verify you have an active GitHub Copilot subscription

**Model not available:**
- Check that your Copilot subscription includes Claude models
- Try a different model from the supported list

**Connection refused:**
- Ensure the proxy is running (`./copilot-proxy`)
- Verify `ANTHROPIC_BASE_URL` points to the correct port

## Credits

Inspired by and compatible with [opencode](https://github.com/crisphwy/opencode) authentication flow.
