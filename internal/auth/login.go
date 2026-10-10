// Browser sign-in with a ChatGPT account.
// Client.Login discovers the endpoints, registers a dynamic client on first use, opens the
// authorize URL with PKCE, waits for the redirect on a local callback server, exchanges the code
// (tokens.go), verifies the ID token (oidc.go) and saves the Credentials. A registration OpenAI
// rejects is forgotten so the next sign-in starts fresh.

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/rzhang57/loop/internal/store"
)

type callback struct {
	code      string
	clientID  string
	errorCode string
	err       error
}

// Login runs the browser sign-in, calling showURL with the page to open, and saves the tokens.
func (c *Client) Login(ctx context.Context, showURL func(string)) (Credentials, error) {
	ep, err := c.discover(ctx)
	if err != nil {
		return Credentials{}, err
	}
	hostID, err := c.hostID(ctx)
	if err != nil {
		return Credentials{}, err
	}
	savedClientID, registered, err := c.store.Get(ctx, keyClientID)
	if err != nil {
		return Credentials{}, err
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", c.cfg.CallbackPort))
	if err != nil {
		return Credentials{}, fmt.Errorf("listen for the sign-in callback on port %d (is another sign-in open?): %w", c.cfg.CallbackPort, err)
	}
	redirectURI := fmt.Sprintf("http://%s%s", listener.Addr(), callbackPath)

	verifier, state, nonce := randomToken(), randomToken(), randomToken()
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {savedClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {requestedScopes},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"ext_agent_host_id":     {hostID},
		"resource":              {APIResource},
	}
	if !registered {
		query.Set("client_id", dynamicClientID)
		query.Set("agent_name_hint", c.cfg.AppName)
	}
	authURL := ep.Authorization + "?" + query.Encode()

	results := make(chan callback, 1)
	server := &http.Server{Handler: callbackHandler(state, results)}
	go server.Serve(listener)
	defer server.Close()

	showURL(authURL)
	_ = c.cfg.OpenBrowser(authURL)

	var result callback
	select {
	case <-ctx.Done():
		return Credentials{}, ctx.Err()
	case result = <-results:
	}
	if result.err != nil {
		if rejectsClient(result.errorCode) {
			return Credentials{}, c.forgetRegistration(ctx, result.err)
		}
		return Credentials{}, result.err
	}

	clientID := savedClientID
	switch {
	case registered && result.clientID != "" && result.clientID != savedClientID:
		return Credentials{}, c.forgetRegistration(ctx, errors.New("sign-in returned an unexpected client ID; try signing in again"))
	case !registered && result.clientID == "":
		return Credentials{}, errors.New("sign-in finished without issuing a client ID")
	case !registered:
		clientID = result.clientID
		// Saved before the exchange so a failed exchange doesn't throw the registration away.
		if err := c.store.Set(ctx, keyClientID, clientID); err != nil {
			return Credentials{}, err
		}
	}

	tok, err := c.requestToken(ctx, ep.Token, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {result.code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"resource":      {APIResource},
	})
	var tokenErr *TokenError
	if errors.As(err, &tokenErr) && rejectsClient(tokenErr.Code) {
		return Credentials{}, c.forgetRegistration(ctx, err)
	}
	if err != nil {
		return Credentials{}, err
	}
	// An omitted scope means the server granted exactly what was requested.
	if tok.Scope != "" && !slices.Contains(strings.Fields(tok.Scope), planScope) {
		return Credentials{}, errors.New("sign-in did not grant use of your ChatGPT plan; allow it on the consent screen and try again")
	}
	claims, err := verifyIDToken(ctx, c.cfg.HTTPClient, ep.JWKS, tok.IDToken, c.cfg.Issuer, clientID, nonce, c.cfg.Now())
	if err != nil {
		return Credentials{}, err
	}

	creds := Credentials{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    c.expiry(tok),
		Email:        claims.Email,
	}
	err = c.store.Update(ctx, func(tx *store.Tx) error { return saveCredentials(ctx, tx, creds) })
	return creds, err
}

// rejectsClient reports whether an OAuth error code means OpenAI no longer accepts this client registration.
func rejectsClient(code string) bool {
	return code == "invalid_client" || code == "unauthorized_client"
}

// forgetRegistration drops a client registration OpenAI no longer accepts, so the next sign-in
// registers a new one, and returns cause.
func (c *Client) forgetRegistration(ctx context.Context, cause error) error {
	return errors.Join(cause, c.store.Delete(context.WithoutCancel(ctx), keyClientID))
}

// hostID returns this machine's stable host identifier, creating it on first use.
func (c *Client) hostID(ctx context.Context) (string, error) {
	var id string
	err := c.store.Update(ctx, func(tx *store.Tx) error {
		saved, ok, err := tx.Get(ctx, keyHostID)
		if err != nil || ok {
			id = saved
			return err
		}
		id = "urn:uuid:" + newUUID()
		return tx.Set(ctx, keyHostID, id)
	})
	return id, err
}

func callbackHandler(state string, results chan<- callback) http.Handler {
	deliver := func(cb callback) {
		select {
		case results <- cb:
		default:
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != callbackPath || q.Get("state") != state {
			http.Error(w, "unexpected sign-in callback", http.StatusBadRequest)
			return
		}
		// Each reply is written before delivering, since delivering lets Login return and close the server.
		if code := q.Get("error"); code != "" {
			fmt.Fprintln(w, "Sign-in was not completed. You can close this tab.")
			deliver(callback{errorCode: code, err: fmt.Errorf("sign-in was not completed: %s %s", code, q.Get("error_description"))})
			return
		}
		if q.Get("code") == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			deliver(callback{err: errors.New("sign-in callback had no authorization code")})
			return
		}
		fmt.Fprintln(w, "Signed in to loop. You can close this tab.")
		deliver(callback{code: q.Get("code"), clientID: q.Get("client_id")})
	})
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func openBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
