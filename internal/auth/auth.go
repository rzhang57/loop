// Package auth signs in with a ChatGPT account and keeps its tokens valid.
// This file holds what every flow shares: the Client, its Config and the settings keys it stores.
// Each job lives in its own file: login.go runs the browser sign-in, refresh.go keeps the access
// token fresh, logout.go signs out, tokens.go talks to the token endpoint, credentials.go reads and
// writes saved tokens, and oidc.go verifies ID tokens. Used by cmd/loop, tui, and llm as its
// TokenSource.
package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rzhang57/loop/internal/store"
)

const (
	DefaultIssuer = "https://auth.openai.com"
	APIResource   = "https://api.openai.com/v1"

	dynamicClientID = "dynamic_agent_client"
	planScope       = "chatgpt.tokens.use.direct"
	requestedScopes = "openid profile email offline_access resource.invoke " + planScope
	callbackPath    = "/auth/callback"
	refreshMargin   = 5 * time.Minute

	keyClientID    = "auth.client_id"
	keyHostID      = "auth.host_id"
	keyCredentials = "auth.credentials"
)

var (
	ErrNotSignedIn    = errors.New("not signed in")
	ErrSessionExpired = errors.New("your ChatGPT session has ended, sign in again")
)

type Config struct {
	Issuer       string
	AppName      string
	CallbackPort int
	HTTPClient   *http.Client
	OpenBrowser  func(url string) error
	Now          func() time.Time
}

// DefaultConfig targets OpenAI's issuer on the documented callback port.
func DefaultConfig() Config {
	return Config{
		Issuer:       DefaultIssuer,
		AppName:      "loop",
		CallbackPort: 1455,
		HTTPClient:   &http.Client{Timeout: 30 * time.Second},
		OpenBrowser:  openBrowser,
		Now:          time.Now,
	}
}

// Client signs in with ChatGPT and keeps the resulting tokens valid in the shared store.
type Client struct {
	cfg   Config
	store *store.Store

	mu        sync.Mutex
	endpoints *endpoints
}

func New(s *store.Store, cfg Config) *Client {
	return &Client{cfg: cfg, store: s}
}

// Current returns the saved credentials, or ErrNotSignedIn.
func (c *Client) Current(ctx context.Context) (Credentials, error) {
	return loadCredentials(ctx, c.store)
}

func (c *Client) discover(ctx context.Context) (endpoints, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endpoints != nil {
		return *c.endpoints, nil
	}
	ep, err := discover(ctx, c.cfg.HTTPClient, c.cfg.Issuer)
	if err != nil {
		return ep, err
	}
	c.endpoints = &ep
	return ep, nil
}
