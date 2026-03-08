// Package auth handles GitHub OAuth device flow and Copilot token management.
package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	//set this i used the one from opencode but im not commiting a key from a different project
	githubClientID = ""
	githubScope    = "read:user"

	deviceCodeURL  = "https://github.com/login/device/code"
	accessTokenURL = "https://github.com/login/oauth/access_token"

	userAgent = "opencode/0.1.0"
)

// opencodeAuthFile is where opencode stores its auth tokens.
var opencodeAuthFile = filepath.Join(
	os.Getenv("HOME"), ".local", "share", "opencode", "auth.json",
)

type opencodeAuth struct {
	Type    string `json:"type"`
	Refresh string `json:"refresh"`
	Access  string `json:"access"`
	Expires int64  `json:"expires"`
}

// Manager holds the GitHub OAuth token used directly for Copilot API calls.
// OpenCode does NOT use a two-step token exchange — the refresh token IS the
// bearer token for api.githubcopilot.com.
type Manager struct {
	Token string // long-lived GitHub OAuth token (gho_...)
}

// NewManager creates an auth manager. It tries to load an existing GitHub
// token from the opencode auth file. If none is found, it initiates the
// GitHub device flow.
func NewManager(forceLogin bool) (*Manager, error) {
	if !forceLogin {
		token, err := loadOpencodeToken()
		if err == nil && token != "" {
			fmt.Printf("Using existing GitHub token from opencode auth (~/.local/share/opencode/auth.json)\n")
			return &Manager{Token: token}, nil
		}
		if err != nil {
			fmt.Printf("No existing token found (%v), starting device flow...\n", err)
		}
	}

	token, err := runDeviceFlow()
	if err != nil {
		return nil, fmt.Errorf("device flow failed: %w", err)
	}
	return &Manager{Token: token}, nil
}

// RefreshToken re-reads the opencode auth file to get the latest token.
// This is called by the proxy handler when Copilot returns a 401.
func (m *Manager) RefreshToken() (string, error) {
	token, err := loadOpencodeToken()
	if err != nil {
		return "", fmt.Errorf("refresh from auth file: %w", err)
	}
	if token == "" {
		return "", fmt.Errorf("empty token in auth file")
	}
	m.Token = token
	return token, nil
}

// Validate makes a test call to confirm the token has Copilot access.
func (m *Manager) Validate() error {
	req, _ := http.NewRequest("GET", "https://api.github.com/copilot_internal/v2/token", nil)
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 200 = has Copilot subscription (old token-exchange path, may still work)
	// 404 = endpoint not found for this account (no subscription or different API)
	// Either way, if we get a non-401/403 response the token itself is valid.
	// The actual API call will tell us if Copilot is unavailable.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("GitHub token rejected (status %d) — try --login to re-authenticate", resp.StatusCode)
	}
	return nil
}

// loadOpencodeToken reads the GitHub OAuth token from opencode's auth.json.
func loadOpencodeToken() (string, error) {
	data, err := os.ReadFile(opencodeAuthFile)
	if err != nil {
		return "", err
	}

	var authMap map[string]opencodeAuth
	if err := json.Unmarshal(data, &authMap); err != nil {
		return "", err
	}

	entry, ok := authMap["github-copilot"]
	if !ok {
		return "", fmt.Errorf("no github-copilot entry in auth.json")
	}
	if entry.Refresh == "" {
		return "", fmt.Errorf("empty refresh token")
	}
	return entry.Refresh, nil
}

// --------------------------------------------------------------------------
// Device Flow
// --------------------------------------------------------------------------

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type accessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
	Interval    int    `json:"interval"`
}

func runDeviceFlow() (string, error) {
	body := url.Values{
		"client_id": {githubClientID},
		"scope":     {githubScope},
	}
	req, _ := http.NewRequest("POST", deviceCodeURL, strings.NewReader(body.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var dcr deviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dcr); err != nil {
		return "", err
	}

	fmt.Printf("\n\033[1mGitHub Authentication Required\033[0m\n")
	fmt.Printf("──────────────────────────────────────────\n")
	fmt.Printf("1. Open:  \033[36m%s\033[0m\n", dcr.VerificationURI)
	fmt.Printf("2. Enter: \033[1;33m%s\033[0m\n", dcr.UserCode)
	fmt.Printf("──────────────────────────────────────────\n")
	fmt.Printf("Waiting for authorization...\n\n")

	interval := dcr.Interval
	if interval < 5 {
		interval = 5
	}

	for {
		time.Sleep(time.Duration(interval)*time.Second + 3*time.Second)

		pollBody := url.Values{
			"client_id":   {githubClientID},
			"device_code": {dcr.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		pr, _ := http.NewRequest("POST", accessTokenURL, strings.NewReader(pollBody.Encode()))
		pr.Header.Set("Accept", "application/json")
		pr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		pr.Header.Set("User-Agent", userAgent)

		presp, err := http.DefaultClient.Do(pr)
		if err != nil {
			return "", err
		}
		raw, _ := io.ReadAll(presp.Body)
		presp.Body.Close()

		var atr accessTokenResponse
		if err := json.Unmarshal(raw, &atr); err != nil {
			return "", err
		}

		switch atr.Error {
		case "":
			if atr.AccessToken != "" {
				fmt.Printf("\033[32mAuthenticated successfully!\033[0m\n\n")
				return atr.AccessToken, nil
			}
		case "authorization_pending":
			// keep polling
		case "slow_down":
			if atr.Interval > 0 {
				interval = atr.Interval
			} else {
				interval += 5
			}
		case "expired_token":
			return "", fmt.Errorf("device code expired — please restart")
		case "access_denied":
			return "", fmt.Errorf("access denied by user")
		default:
			return "", fmt.Errorf("OAuth error: %s — %s", atr.Error, atr.ErrorDesc)
		}
	}
}
