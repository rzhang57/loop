// Signing out.
// Logout deletes the saved tokens but keeps the client registration for the next sign-in, then
// revokes the refresh token with OpenAI. With nothing to sign out of it clears the registration
// instead, as an escape hatch when OpenAI stops accepting it.

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/rzhang57/loop/internal/store"
)

// Logout forgets the saved tokens, keeping the client registration for the next sign-in as OpenAI
// asks, then revokes the session. With nothing to sign out of it returns ErrNotSignedIn and forgets
// the registration instead, so a registration OpenAI stopped accepting can always be cleared.
// A revoke error is returned only after the local sign-out has already happened.
func (c *Client) Logout(ctx context.Context) error {
	var creds Credentials
	var clientID string
	signedIn := true
	err := c.store.Update(ctx, func(tx *store.Tx) error {
		var err error
		creds, err = loadCredentials(ctx, tx)
		if errors.Is(err, ErrNotSignedIn) {
			signedIn = false
			return tx.Delete(ctx, keyClientID)
		}
		if err != nil {
			return err
		}
		if clientID, _, err = tx.Get(ctx, keyClientID); err != nil {
			return err
		}
		return tx.Delete(ctx, keyCredentials)
	})
	if err != nil {
		return err
	}
	if !signedIn {
		return ErrNotSignedIn
	}
	if err := c.revoke(ctx, clientID, creds.RefreshToken); err != nil {
		return fmt.Errorf("signed out on this machine, but OpenAI didn't confirm the session was revoked: %w", err)
	}
	return nil
}

func (c *Client) revoke(ctx context.Context, clientID, refreshToken string) error {
	ep, err := c.discover(ctx)
	if err != nil {
		return err
	}
	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}}
	resp, err := c.postForm(ctx, ep.Revocation, form)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke session: %s", resp.Status)
	}
	return nil
}
