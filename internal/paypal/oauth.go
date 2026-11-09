package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	sandboxBaseURL    = "https://api-m.sandbox.paypal.com"
	productionBaseURL = "https://api-m.paypal.com"

	sandboxOAuthURL    = sandboxBaseURL + "/v1/oauth2/token"
	productionOAuthURL = productionBaseURL + "/v1/oauth2/token"
)

// apiBaseURL returns the REST API base URL for the given environment. It is used
// for direct Invoicing API calls that the MCP server does not expose.
func apiBaseURL(environment string) string {
	if strings.ToUpper(environment) == "PRODUCTION" {
		return productionBaseURL
	}
	return sandboxBaseURL
}

// TokenResponse represents the JSON returned by PayPal OAuth2 token endpoint.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// FetchToken mints a sandbox or production bearer token using client
// credentials. The response carries the token lifetime so callers can refresh
// before it expires.
func FetchToken(ctx context.Context, clientID, secret, environment string) (*TokenResponse, error) {
	endpoint := sandboxOAuthURL
	if strings.ToUpper(environment) == "PRODUCTION" {
		endpoint = productionOAuthURL
	}
	return fetchTokenFromURL(ctx, endpoint, clientID, secret, &http.Client{Timeout: 10 * time.Second})
}

func fetchTokenFromURL(ctx context.Context, endpoint, clientID, secret string, client *http.Client) (*TokenResponse, error) {
	if clientID == "" || secret == "" {
		return nil, fmt.Errorf("paypal: client ID and secret required")
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("paypal: create token request: %w", err)
	}
	req.SetBasicAuth(clientID, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paypal: oauth request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("paypal: read oauth body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("paypal: oauth error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("paypal: decode oauth response: %w", err)
	}

	if tr.AccessToken == "" {
		return nil, fmt.Errorf("paypal: empty access token returned")
	}

	return &tr, nil
}
