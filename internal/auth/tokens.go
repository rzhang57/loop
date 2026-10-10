// Requests to the OAuth token endpoint and the errors it returns.
// requestToken posts a form and decodes either the tokens or a TokenError, which understands both
// error shapes OpenAI uses and knows which codes mean the session is over. expiry turns a token
// response's lifetime into a deadline.

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// TokenError is an OAuth error returned by the token endpoint.
type TokenError struct {
	Status      int
	Code        string
	Description string
}

// UnmarshalJSON accepts both the standard OAuth shape and one where "error" is an object with a code.
func (e *TokenError) UnmarshalJSON(data []byte) error {
	var body struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	e.Description = body.Description
	if json.Unmarshal(body.Error, &e.Code) == nil {
		return nil
	}
	var nested struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body.Error, &nested); err != nil {
		return err
	}
	e.Code = nested.Code
	if e.Description == "" {
		e.Description = nested.Message
	}
	return nil
}

func (e *TokenError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("token request failed (%d %s): %s", e.Status, e.Code, e.Description)
	}
	return fmt.Sprintf("token request failed (%d %s)", e.Status, e.Code)
}

func (e *TokenError) sessionEnded() bool {
	return slices.Contains([]string{
		"invalid_grant", "invalid_refresh_token", "token_expired",
		"refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused",
	}, e.Code)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// expiry falls back to the documented one-hour lifetime when the response leaves expires_in out.
func (c *Client) expiry(tok tokenResponse) time.Time {
	lifetime := time.Duration(tok.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	return c.cfg.Now().Add(lifetime)
}

func (c *Client) requestToken(ctx context.Context, endpoint string, form url.Values) (tokenResponse, error) {
	var tok tokenResponse
	resp, err := c.postForm(ctx, endpoint, form)
	if err != nil {
		return tok, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tokenErr := &TokenError{Status: resp.StatusCode}
		_ = json.NewDecoder(resp.Body).Decode(tokenErr)
		return tok, tokenErr
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return tok, fmt.Errorf("decode token response: %w", err)
	}
	return tok, nil
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.cfg.HTTPClient.Do(req)
}
