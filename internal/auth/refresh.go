// Keeps the access token usable.
// AccessToken refreshes it shortly before it expires and RefreshRejected replaces one the API
// turned down. Both go through refresh, which spends the single-use refresh token inside a store
// write lock and re-reads the saved tokens first, so concurrent processes never spend the same one.
// A dead refresh token signs the user out with ErrSessionExpired.

package auth

import (
	"context"
	"errors"
	"net/url"

	"github.com/rzhang57/loop/internal/store"
)

// AccessToken returns a usable access token, refreshing it first when it is close to expiring.
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	creds, err := loadCredentials(ctx, c.store)
	if err != nil {
		return "", err
	}
	if creds.fresh(c.cfg.Now()) {
		return creds.AccessToken, nil
	}
	return c.refresh(ctx, func(current Credentials) bool { return !current.fresh(c.cfg.Now()) })
}

// RefreshRejected replaces an access token the API rejected, unless another process already replaced it.
func (c *Client) RefreshRejected(ctx context.Context, rejected string) (string, error) {
	return c.refresh(ctx, func(current Credentials) bool { return current.AccessToken == rejected })
}

// refresh swaps the refresh token for new tokens while holding the store's write lock, so two
// processes never spend the same single-use refresh token.
func (c *Client) refresh(ctx context.Context, needed func(Credentials) bool) (string, error) {
	// Once the old refresh token is spent the new one must be saved, so cancelling can't stop it midway.
	ctx = context.WithoutCancel(ctx)
	ep, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	var accessToken string
	var ended bool
	err = c.store.Update(ctx, func(tx *store.Tx) error {
		creds, err := loadCredentials(ctx, tx)
		if err != nil {
			return err
		}
		if !needed(creds) {
			accessToken = creds.AccessToken
			return nil
		}
		clientID, _, err := tx.Get(ctx, keyClientID)
		if err != nil {
			return err
		}
		tok, err := c.requestToken(ctx, ep.Token, url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {clientID},
			"refresh_token": {creds.RefreshToken},
			"resource":      {APIResource},
		})
		var tokenErr *TokenError
		if errors.As(err, &tokenErr) && tokenErr.sessionEnded() {
			ended = true
			return tx.Delete(ctx, keyCredentials)
		}
		if err != nil {
			return err
		}
		creds.AccessToken = tok.AccessToken
		creds.ExpiresAt = c.expiry(tok)
		if tok.RefreshToken != "" {
			creds.RefreshToken = tok.RefreshToken
		}
		accessToken = creds.AccessToken
		return saveCredentials(ctx, tx, creds)
	})
	if err != nil {
		return "", err
	}
	if ended {
		return "", ErrSessionExpired
	}
	return accessToken, nil
}
