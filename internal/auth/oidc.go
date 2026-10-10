// OpenID Connect helpers for the sign-in flow in login.go.
// discover fetches the issuer's well-known configuration for its authorize, token, revocation and
// key endpoints. verifyIDToken checks an RS256 ID token's signature against the issuer's published
// JWKS, then its issuer, audience, expiry and nonce, and returns the claims (notably the email).

package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"
)

const clockSkew = time.Minute

type endpoints struct {
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Revocation    string `json:"revocation_endpoint"`
	JWKS          string `json:"jwks_uri"`
}

type idClaims struct {
	Issuer   string   `json:"iss"`
	Audience audience `json:"aud"`
	Expiry   int64    `json:"exp"`
	Nonce    string   `json:"nonce"`
	Email    string   `json:"email"`
}

// audience accepts the "aud" claim as either a single string or a list.
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audience{single}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*a = list
	return nil
}

type jwk struct {
	KeyID string `json:"kid"`
	Type  string `json:"kty"`
	N     string `json:"n"`
	E     string `json:"e"`
}

func discover(ctx context.Context, client *http.Client, issuer string) (endpoints, error) {
	var e endpoints
	err := getJSON(ctx, client, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", &e)
	if err != nil {
		return e, fmt.Errorf("discover %s: %w", issuer, err)
	}
	return e, nil
}

// verifyIDToken checks an RS256 ID token's signature against the issuer's published keys, then its claims.
func verifyIDToken(ctx context.Context, client *http.Client, jwksURL, token, issuer, clientID, nonce string, now time.Time) (idClaims, error) {
	var claims idClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, errors.New("id token is not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return claims, fmt.Errorf("id token header: %w", err)
	}
	if header.Alg != "RS256" {
		return claims, fmt.Errorf("id token uses unsupported algorithm %q", header.Alg)
	}

	var keySet struct {
		Keys []jwk `json:"keys"`
	}
	if err := getJSON(ctx, client, jwksURL, &keySet); err != nil {
		return claims, fmt.Errorf("fetch signing keys: %w", err)
	}
	i := slices.IndexFunc(keySet.Keys, func(k jwk) bool { return k.KeyID == header.Kid && k.Type == "RSA" })
	if i < 0 {
		return claims, fmt.Errorf("no signing key with id %q", header.Kid)
	}
	key, err := rsaPublicKey(keySet.Keys[i])
	if err != nil {
		return claims, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, fmt.Errorf("id token signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return claims, errors.New("id token signature is invalid")
	}

	if err := decodeSegment(parts[1], &claims); err != nil {
		return claims, fmt.Errorf("id token claims: %w", err)
	}
	switch {
	case claims.Issuer != issuer:
		return claims, fmt.Errorf("id token issuer is %q, want %q", claims.Issuer, issuer)
	case !slices.Contains(claims.Audience, clientID):
		return claims, errors.New("id token was not issued to this client")
	case now.After(time.Unix(claims.Expiry, 0).Add(clockSkew)):
		return claims, errors.New("id token has expired")
	case claims.Nonce != nonce:
		return claims, errors.New("id token nonce does not match this sign-in")
	}
	return claims, nil
}

func rsaPublicKey(k jwk) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("signing key modulus: %w", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("signing key exponent: %w", err)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
}

func decodeSegment(segment string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
