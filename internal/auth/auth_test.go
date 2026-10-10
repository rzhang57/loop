package auth

import (
	"cmp"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rzhang57/loop/internal/store"
)

type fakeIssuer struct {
	t             *testing.T
	server        *httptest.Server
	signingKey    *rsa.PrivateKey
	tamperKey     *rsa.PrivateKey
	grantScope    string
	issuedID      string
	lastAuthorize url.Values

	denyConsent      bool
	returnedClientID string
	refreshFailure   string
	revokeStatus     int
	rejectClient     string
	rejectCode       string
	onRefresh        func()

	mu           sync.Mutex
	codes        map[string]url.Values
	liveRefresh  map[string]bool
	refreshCalls atomic.Int32
	revoked      []string
	nextToken    int
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIssuer{
		t:           t,
		signingKey:  key,
		grantScope:  requestedScopes,
		issuedID:    "oaiapp_test",
		codes:       map[string]url.Values{},
		liveRefresh: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": f.server.URL + "/authorize",
			"token_endpoint":         f.server.URL + "/token",
			"revocation_endpoint":    f.server.URL + "/revoke",
			"jwks_uri":               f.server.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1",
			"kty": "RSA",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Form.Get("token"))
		f.mu.Unlock()
		if f.revokeStatus != 0 {
			w.WriteHeader(f.revokeStatus)
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIssuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.lastAuthorize = q
	code := fmt.Sprintf("code-%d", len(f.codes))
	f.codes[code] = q
	f.mu.Unlock()

	back := url.Values{"code": {code}, "state": {q.Get("state")}}
	switch {
	case f.denyConsent:
		back = url.Values{"error": {"access_denied"}, "state": {q.Get("state")}}
	case f.returnedClientID != "":
		back.Set("client_id", f.returnedClientID)
	case q.Get("client_id") == dynamicClientID:
		back.Set("client_id", f.issuedID)
	}
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
}

func (f *fakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	fail := func(code string) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		if r.Form.Get("client_id") == f.rejectClient {
			fail(cmp.Or(f.rejectCode, "invalid_client"))
			return
		}
		authorize, ok := f.codes[r.Form.Get("code")]
		challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || authorize.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) {
			fail("invalid_grant")
			return
		}
		delete(f.codes, r.Form.Get("code"))
		f.writeTokens(w, r.Form.Get("client_id"), authorize.Get("nonce"))
	case "refresh_token":
		f.refreshCalls.Add(1)
		if f.onRefresh != nil {
			f.onRefresh()
		}
		if f.refreshFailure != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, f.refreshFailure)
			return
		}
		if !f.liveRefresh[r.Form.Get("refresh_token")] {
			fail("invalid_grant")
			return
		}
		delete(f.liveRefresh, r.Form.Get("refresh_token"))
		f.writeTokens(w, r.Form.Get("client_id"), "")
	default:
		fail("unsupported_grant_type")
	}
}

func (f *fakeIssuer) writeTokens(w http.ResponseWriter, clientID, nonce string) {
	f.nextToken++
	refresh := fmt.Sprintf("refresh-%d", f.nextToken)
	f.liveRefresh[refresh] = true
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  fmt.Sprintf("access-%d", f.nextToken),
		"refresh_token": refresh,
		"id_token":      f.idToken(clientID, nonce),
		"expires_in":    3600,
		"scope":         f.grantScope,
	})
}

func (f *fakeIssuer) idToken(clientID, nonce string) string {
	return f.sign(map[string]string{"alg": "RS256", "kid": "k1"}, f.claims(clientID, nonce))
}

func (f *fakeIssuer) claims(clientID, nonce string) map[string]any {
	return map[string]any{
		"iss": f.server.URL, "sub": "user-1", "aud": clientID, "nonce": nonce,
		"email": "me@example.com", "exp": time.Now().Add(time.Hour).Unix(),
	}
}

func (f *fakeIssuer) sign(headerFields map[string]string, claimFields map[string]any) string {
	header, _ := json.Marshal(headerFields)
	claims, _ := json.Marshal(claimFields)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	key := f.signingKey
	if f.tamperKey != nil {
		key = f.tamperKey
	}
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestClient(t *testing.T, f *fakeIssuer, dbPath string, clk *clock) *Client {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s, Config{
		Issuer:       f.server.URL,
		AppName:      "loop",
		CallbackPort: 0,
		HTTPClient:   f.server.Client(),
		OpenBrowser: func(authURL string) error {
			go func() {
				resp, err := http.Get(authURL)
				if err == nil {
					resp.Body.Close()
				}
			}()
			return nil
		},
		Now: clk.Now,
	})
}

func setup(t *testing.T) (*fakeIssuer, *Client, string, *clock) {
	f := newFakeIssuer(t)
	clk := &clock{now: time.Now()}
	dbPath := filepath.Join(t.TempDir(), "loop.db")
	return f, newTestClient(t, f, dbPath, clk), dbPath, clk
}

func login(t *testing.T, c *Client) Credentials {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	creds, err := c.Login(ctx, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func TestLoginRegistersThenReusesClientID(t *testing.T) {
	f, c, _, _ := setup(t)

	creds := login(t, c)
	if creds.AccessToken == "" || creds.Email != "me@example.com" {
		t.Fatalf("unexpected credentials %+v", creds)
	}
	first := f.lastAuthorize
	if first.Get("client_id") != dynamicClientID || first.Get("agent_name_hint") != "loop" {
		t.Fatalf("first sign-in should register dynamically, got %v", first)
	}
	hostID := first.Get("ext_agent_host_id")
	if !strings.HasPrefix(hostID, "urn:uuid:") {
		t.Fatalf("host id %q is not a urn:uuid", hostID)
	}
	if first.Get("resource") != APIResource || first.Get("code_challenge_method") != "S256" {
		t.Fatalf("missing resource or PKCE parameters: %v", first)
	}

	login(t, c)
	second := f.lastAuthorize
	if second.Get("client_id") != f.issuedID || second.Has("agent_name_hint") {
		t.Fatalf("second sign-in should reuse the issued client id, got %v", second)
	}
	if second.Get("ext_agent_host_id") != hostID {
		t.Fatal("host id changed between sign-ins")
	}
}

func TestLoginRejectsForgedIDToken(t *testing.T) {
	f, c, _, _ := setup(t)
	f.tamperKey, _ = rsa.GenerateKey(rand.Reader, 2048)

	_, err := c.Login(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("got %v, want a signature error", err)
	}
	if _, err := c.Current(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("credentials were saved despite a forged id token")
	}
}

func TestLoginRequiresPlanScope(t *testing.T) {
	f, c, _, _ := setup(t)
	f.grantScope = "openid profile email offline_access"

	_, err := c.Login(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "ChatGPT plan") {
		t.Fatalf("got %v, want a missing plan scope error", err)
	}
}

func TestAccessTokenRefreshesOnlyWhenNearExpiry(t *testing.T) {
	f, c, _, clk := setup(t)
	creds := login(t, c)
	ctx := context.Background()

	token, err := c.AccessToken(ctx)
	if err != nil || token != creds.AccessToken || f.refreshCalls.Load() != 0 {
		t.Fatalf("fresh token should be reused: token=%s err=%v calls=%d", token, err, f.refreshCalls.Load())
	}

	clk.advance(56 * time.Minute)
	token, err = c.AccessToken(ctx)
	if err != nil || token == creds.AccessToken || f.refreshCalls.Load() != 1 {
		t.Fatalf("token near expiry should refresh: token=%s err=%v calls=%d", token, err, f.refreshCalls.Load())
	}
	saved, _ := c.Current(ctx)
	if saved.AccessToken != token || saved.RefreshToken == creds.RefreshToken {
		t.Fatal("rotated tokens were not saved")
	}
}

func TestConcurrentRefreshSpendsRefreshTokenOnce(t *testing.T) {
	f, first, dbPath, clk := setup(t)
	login(t, first)
	second := newTestClient(t, f, dbPath, clk)
	clk.advance(time.Hour)

	var wg sync.WaitGroup
	tokens := make([]string, 2)
	errs := make([]error, 2)
	for i, c := range []*Client{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = c.AccessToken(context.Background())
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if tokens[0] != tokens[1] {
		t.Fatalf("processes ended up with different tokens: %v", tokens)
	}
	if calls := f.refreshCalls.Load(); calls != 1 {
		t.Fatalf("refresh endpoint called %d times, want 1", calls)
	}
}

func TestRefreshRejectedSkipsWhenAlreadyReplaced(t *testing.T) {
	f, c, _, _ := setup(t)
	creds := login(t, c)
	ctx := context.Background()

	replaced, err := c.RefreshRejected(ctx, creds.AccessToken)
	if err != nil || replaced == creds.AccessToken {
		t.Fatalf("rejected token should be replaced: %s %v", replaced, err)
	}
	again, err := c.RefreshRejected(ctx, creds.AccessToken)
	if err != nil || again != replaced || f.refreshCalls.Load() != 1 {
		t.Fatalf("stale rejection should reuse the replacement: %s %v calls=%d", again, err, f.refreshCalls.Load())
	}
}

func TestDeadRefreshTokenSignsOut(t *testing.T) {
	f, c, _, clk := setup(t)
	login(t, c)
	f.mu.Lock()
	clear(f.liveRefresh)
	f.mu.Unlock()
	clk.advance(time.Hour)
	ctx := context.Background()

	if _, err := c.AccessToken(ctx); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v, want ErrSessionExpired", err)
	}
	if _, err := c.Current(ctx); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("dead credentials were kept")
	}
}

func TestLogoutRevokesAndForgets(t *testing.T) {
	f, c, _, _ := setup(t)
	creds := login(t, c)
	ctx := context.Background()

	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.revoked) != 1 || f.revoked[0] != creds.RefreshToken {
		t.Fatalf("revoked %v, want the refresh token", f.revoked)
	}
	if _, err := c.Current(ctx); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("credentials survived logout")
	}
	if _, ok, _ := c.store.Get(ctx, keyClientID); !ok {
		t.Fatal("logout should keep the client registration for the next sign-in")
	}
}

func TestLoginIgnoresCallbacksWithWrongState(t *testing.T) {
	f, c, _, _ := setup(t)
	c.cfg.OpenBrowser = func(authURL string) error {
		go func() {
			u, _ := url.Parse(authURL)
			redirect := u.Query().Get("redirect_uri")
			resp, err := http.Get(redirect + "?code=attacker&state=forged")
			if err == nil {
				if resp.StatusCode != http.StatusBadRequest {
					t.Errorf("forged callback got %d, want 400", resp.StatusCode)
				}
				resp.Body.Close()
			}
			if resp, err := http.Get(authURL); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}

	creds := login(t, c)
	if creds.AccessToken == "" || len(f.codes) != 0 {
		t.Fatal("the real callback should still complete the sign-in")
	}
}

func TestLoginRejectsCallbackWithoutCode(t *testing.T) {
	_, c, _, _ := setup(t)
	c.cfg.OpenBrowser = callbackWith(url.Values{})
	_, err := c.Login(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "no authorization code") {
		t.Fatalf("got %v, want a missing code error", err)
	}
}

func TestLoginReportsDeniedConsent(t *testing.T) {
	f, c, _, _ := setup(t)
	f.denyConsent = true

	_, err := c.Login(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("got %v, want the denial reported", err)
	}
}

func TestLoginKeepsRegistrationWhenExchangeFails(t *testing.T) {
	f, c, _, _ := setup(t)
	f.grantScope = "openid"

	if _, err := c.Login(context.Background(), func(string) {}); err == nil {
		t.Fatal("login without the plan scope should fail")
	}
	f.grantScope = requestedScopes
	login(t, c)
	if f.lastAuthorize.Get("client_id") != f.issuedID {
		t.Fatalf("retry should reuse the issued client, sent %q", f.lastAuthorize.Get("client_id"))
	}
}

func TestLoginRejectsUnexpectedClientID(t *testing.T) {
	f, c, _, _ := setup(t)
	login(t, c)
	f.returnedClientID = "oaiapp_someone_else"

	_, err := c.Login(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "unexpected client ID") {
		t.Fatalf("got %v, want a client ID mismatch error", err)
	}
	if _, ok, _ := c.store.Get(context.Background(), keyClientID); ok {
		t.Fatal("a rejected registration should be forgotten so the next sign-in starts fresh")
	}
}

func TestLoginRecoversFromRejectedRegistration(t *testing.T) {
	f, c, _, _ := setup(t)
	login(t, c)
	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rejectClient = f.issuedID
	f.rejectCode = "unauthorized_client"
	f.mu.Unlock()

	if _, err := c.Login(context.Background(), func(string) {}); err == nil || !strings.Contains(err.Error(), "unauthorized_client") {
		t.Fatalf("got %v, want unauthorized_client", err)
	}
	f.mu.Lock()
	f.issuedID = "oaiapp_fresh"
	f.mu.Unlock()
	login(t, c)
	if f.lastAuthorize.Get("client_id") != dynamicClientID {
		t.Fatalf("after a rejection the next sign-in should register afresh, sent %q", f.lastAuthorize.Get("client_id"))
	}
}

func TestVerifyIDTokenRejectsBadClaims(t *testing.T) {
	f := newFakeIssuer(t)
	now := time.Now()
	header := map[string]string{"alg": "RS256", "kid": "k1"}
	with := func(key string, value any) map[string]any {
		claims := f.claims("client", "nonce")
		claims[key] = value
		return claims
	}
	cases := map[string]string{
		"valid":        f.sign(header, f.claims("client", "nonce")),
		"wrong nonce":  f.sign(header, with("nonce", "other")),
		"wrong aud":    f.sign(header, with("aud", "other-client")),
		"wrong issuer": f.sign(header, with("iss", "https://evil.example")),
		"expired":      f.sign(header, with("exp", now.Add(-2*time.Minute).Unix())),
		"alg none":     f.sign(map[string]string{"alg": "none", "kid": "k1"}, f.claims("client", "nonce")),
		"unknown kid":  f.sign(map[string]string{"alg": "RS256", "kid": "k9"}, f.claims("client", "nonce")),
		"not a jwt":    "abc.def",
	}
	for name, token := range cases {
		_, err := verifyIDToken(context.Background(), f.server.Client(), f.server.URL+"/jwks", token, f.server.URL, "client", "nonce", now)
		if (err == nil) != (name == "valid") {
			t.Errorf("%s: got err=%v", name, err)
		}
	}

	slightlyExpired := f.sign(header, with("exp", now.Add(-30*time.Second).Unix()))
	if _, err := verifyIDToken(context.Background(), f.server.Client(), f.server.URL+"/jwks", slightlyExpired, f.server.URL, "client", "nonce", now); err != nil {
		t.Errorf("30s of clock skew should be tolerated: %v", err)
	}
}

func TestReusedRefreshTokenSignsOut(t *testing.T) {
	f, c, _, clk := setup(t)
	login(t, c)
	f.refreshFailure = `{"error":{"code":"refresh_token_reused","message":"already used"}}`
	clk.advance(time.Hour)

	if _, err := c.AccessToken(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("got %v, want ErrSessionExpired", err)
	}
	if _, err := c.Current(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("credentials should be cleared")
	}
}

func TestOtherRefreshErrorsKeepTheSession(t *testing.T) {
	f, c, _, clk := setup(t)
	login(t, c)
	f.refreshFailure = `{"error":"temporarily_unavailable"}`
	clk.advance(time.Hour)

	_, err := c.AccessToken(context.Background())
	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) || tokenErr.Code != "temporarily_unavailable" {
		t.Fatalf("got %v, want the token error passed through", err)
	}
	if _, err := c.Current(context.Background()); err != nil {
		t.Fatal("a transient failure must not sign the user out")
	}
}

func TestRefreshCompletesWhenCancelledMidway(t *testing.T) {
	f, c, _, clk := setup(t)
	login(t, c)
	clk.advance(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	f.onRefresh = cancel

	token, err := c.AccessToken(ctx)
	if err != nil {
		t.Fatalf("refresh should finish despite cancellation: %v", err)
	}
	saved, _ := c.Current(context.Background())
	if saved.AccessToken != token || !f.liveRefresh[saved.RefreshToken] {
		t.Fatal("the rotated refresh token was not saved")
	}
}

func TestLogoutStillSignsOutLocallyWhenRevokeFails(t *testing.T) {
	f, c, _, _ := setup(t)
	login(t, c)
	f.revokeStatus = http.StatusServiceUnavailable

	err := c.Logout(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signed out on this machine") {
		t.Fatalf("got %v, want a revoke warning", err)
	}
	if _, err := c.Current(context.Background()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("credentials should be gone even though revoke failed")
	}
}

func callbackWith(params url.Values) func(string) error {
	return func(authURL string) error {
		go func() {
			u, _ := url.Parse(authURL)
			params.Set("state", u.Query().Get("state"))
			if resp, err := http.Get(u.Query().Get("redirect_uri") + "?" + params.Encode()); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestCallbackRejectingClientForgetsRegistration(t *testing.T) {
	_, c, _, _ := setup(t)
	login(t, c)
	ctx := context.Background()

	c.cfg.OpenBrowser = callbackWith(url.Values{"error": {"access_denied"}, "error_description": {"not an invalid_client problem"}})
	if _, err := c.Login(ctx, func(string) {}); err == nil {
		t.Fatal("denied consent should fail")
	}
	if _, ok, _ := c.store.Get(ctx, keyClientID); !ok {
		t.Fatal("a description mentioning invalid_client must not drop the registration")
	}

	c.cfg.OpenBrowser = callbackWith(url.Values{"error": {"invalid_client"}})
	if _, err := c.Login(ctx, func(string) {}); err == nil {
		t.Fatal("a rejected client should fail")
	}
	if _, ok, _ := c.store.Get(ctx, keyClientID); ok {
		t.Fatal("a registration OpenAI rejected should be forgotten")
	}
}

func TestLogoutWhenSignedOutClearsRegistration(t *testing.T) {
	_, c, _, _ := setup(t)
	login(t, c)
	ctx := context.Background()
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}

	if err := c.Logout(ctx); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("got %v, want ErrNotSignedIn", err)
	}
	if _, ok, _ := c.store.Get(ctx, keyClientID); ok {
		t.Fatal("logging out while signed out should clear the registration as an escape hatch")
	}
}
