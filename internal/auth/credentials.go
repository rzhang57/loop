package auth

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rzhang57/loop/internal/store"
)

// Credentials are the tokens from a ChatGPT sign-in.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Email        string    `json:"email"`
}

func (c Credentials) fresh(now time.Time) bool {
	return now.Add(refreshMargin).Before(c.ExpiresAt)
}

type settingsReader interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

func loadCredentials(ctx context.Context, s settingsReader) (Credentials, error) {
	var creds Credentials
	raw, ok, err := s.Get(ctx, keyCredentials)
	if err != nil {
		return creds, err
	}
	if !ok {
		return creds, ErrNotSignedIn
	}
	return creds, json.Unmarshal([]byte(raw), &creds)
}

func saveCredentials(ctx context.Context, tx *store.Tx, creds Credentials) error {
	raw, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return tx.Set(ctx, keyCredentials, string(raw))
}
