package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	envTestIssuer = "GX_TEST_AUTH_ISSUER"

	codexIssuer = "https://auth.openai.com"
	grokIssuer  = "https://auth.x.ai"

	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	grokClientID  = "b1a00492-073a-47ea-816f-4c329264a828"

	codexScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	grokScope  = "openid profile email offline_access grok-cli:access api:access"

	codexCallbackPath = "/auth/callback"
	grokCallbackPath  = "/callback"

	jwtAuthClaim = "https://api.openai.com/auth"
)

var (
	errUnknownProvider   = errors.New("unknown auth provider")
	errStateMismatch     = errors.New("oauth state mismatch")
	errInvalidCallback   = errors.New("invalid oauth callback")
	errAccessDenied      = errors.New("authorization denied")
	errInvalidToken      = errors.New("invalid token response")
	errInvalidTestIssuer = errors.New("invalid GX_TEST_AUTH_ISSUER")
	errAccountChanged    = errors.New("oauth account changed")
)

// endpoint is a provider's OAuth URLs. When GX_TEST_AUTH_ISSUER names a
// loopback HTTP issuer, every URL is derived from it so tests never leave the
// machine. Otherwise the production issuer is used.
type endpoint struct {
	issuer    string
	authorize string
	token     string
	userinfo  string
	revoke    string
}

type providerConfig struct {
	name         string
	label        string
	clientID     string
	scope        string
	authorizeRel string
	tokenRel     string
	userinfoRel  string
	revokeRel    string
	callbackPath string
	redirectHost string
	refreshJSON  bool
}

func lookupProvider(name string) (providerConfig, error) {
	switch name {
	case providerCodex:
		return providerConfig{
			name:         providerCodex,
			label:        "Codex",
			clientID:     codexClientID,
			scope:        codexScope,
			authorizeRel: "/oauth/authorize",
			tokenRel:     "/oauth/token",
			revokeRel:    "/oauth/revoke",
			callbackPath: codexCallbackPath,
			redirectHost: "localhost",
			refreshJSON:  true,
		}, nil
	case providerGrok:
		return providerConfig{
			name:         providerGrok,
			label:        "Grok",
			clientID:     grokClientID,
			scope:        grokScope,
			authorizeRel: "/oauth2/authorize",
			tokenRel:     "/oauth2/token",
			userinfoRel:  "/oauth2/userinfo",
			revokeRel:    "/oauth2/revoke",
			callbackPath: grokCallbackPath,
			redirectHost: "127.0.0.1",
			refreshJSON:  false,
		}, nil
	default:
		return providerConfig{}, fmt.Errorf("%w: %q", errUnknownProvider, name)
	}
}

func resolveEndpoint(provider string) (providerConfig, endpoint, error) {
	cfg, err := lookupProvider(provider)
	if err != nil {
		return providerConfig{}, endpoint{}, err
	}
	issuer := codexIssuer
	if provider == providerGrok {
		issuer = grokIssuer
	}
	if raw := strings.TrimSpace(os.Getenv(envTestIssuer)); raw != "" {
		parsed, err := testIssuer(raw)
		if err != nil {
			return providerConfig{}, endpoint{}, err
		}
		issuer = parsed
	}
	ep := endpoint{
		issuer:    issuer,
		authorize: issuer + cfg.authorizeRel,
		token:     issuer + cfg.tokenRel,
		revoke:    issuer + cfg.revokeRel,
	}
	if cfg.userinfoRel != "" {
		ep.userinfo = issuer + cfg.userinfoRel
	}
	return cfg, ep, nil
}

func testIssuer(raw string) (string, error) {
	if !isLoopbackHTTP(raw) {
		return "", errInvalidTestIssuer
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errInvalidTestIssuer
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopbackHTTP(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") || u.User != nil || u.Port() == "" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || strings.EqualFold(host, "localhost") || host == "::1"
}

// Login binds 127.0.0.1 on an ephemeral port, prints the authorize URL to out,
// and waits until the browser hits the redirect or ctx ends. The caller owns
// the deadline (fx waits five minutes). The code is exchanged and the
// credential is saved under root.
func Login(ctx context.Context, root, provider string, out io.Writer) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	if out == nil {
		return Credential{}, errors.New("auth: nil writer")
	}
	cfg, ep, err := resolveEndpoint(provider)
	if err != nil {
		return Credential{}, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Credential{}, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	verifier, err := randomSecret()
	if err != nil {
		_ = ln.Close()
		return Credential{}, err
	}
	state, err := randomSecret()
	if err != nil {
		_ = ln.Close()
		return Credential{}, err
	}
	redirect := fmt.Sprintf("http://%s:%d%s", cfg.redirectHost, port, cfg.callbackPath)
	authURL := authorizeURL(cfg, ep.authorize, redirect, pkceChallenge(verifier), state)

	wait := &callbackWait{ch: make(chan callbackResult, 1)}
	srv := &http.Server{
		Handler:           callbackHandler(cfg.callbackPath, state, wait),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	srv.SetKeepAlivesEnabled(false)
	serveDone := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(serveDone)
	}()
	defer func() {
		_ = srv.Close()
		<-serveDone
	}()

	msg := fmt.Sprintf("Open this URL to sign in with %s:\n%s\n\nWaiting for browser authorization...\n", cfg.label, authURL)
	if _, err := io.WriteString(out, msg); err != nil {
		return Credential{}, err
	}

	var code string
	select {
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	case res := <-wait.ch:
		if res.err != nil {
			return Credential{}, res.err
		}
		code = res.code
	}
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	cred, err := exchangeCode(ctx, cfg, ep, code, verifier, redirect)
	if err != nil {
		return Credential{}, err
	}
	cred.root = root
	if err := Save(root, cred); err != nil {
		return Credential{}, err
	}
	return cred, nil
}

// Logout revokes the saved refresh token when one is present, then deletes
// the credential file. A failed revocation leaves the file in place.
func Logout(ctx context.Context, root, provider string) error {
	cred, err := Load(root, provider)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if cred.RefreshToken != "" {
		if err := revoke(ctx, cred); err != nil {
			return err
		}
	}
	return Delete(root, provider)
}

// Authorization returns "Bearer " plus the access token. An expired credential
// that still has a refresh token is refreshed first. A credential loaded or
// saved with a root persists the rotated token.
func Authorization(cred Credential) string {
	token := cred.AccessToken
	if cred.RefreshToken != "" && expired(cred, time.Now()) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if next, err := refresh(ctx, cred); err == nil {
			token = next.AccessToken
			if cred.root != "" {
				next.root = cred.root
				_ = Save(cred.root, next)
			}
		}
	}
	return "Bearer " + token
}

func expired(cred Credential, now time.Time) bool {
	return !cred.Expiry.IsZero() && !now.Before(cred.Expiry)
}

type callbackResult struct {
	code string
	err  error
}

type callbackWait struct {
	once sync.Once
	ch   chan callbackResult
}

func (w *callbackWait) send(res callbackResult) {
	w.once.Do(func() { w.ch <- res })
}

func callbackHandler(path, state string, wait *callbackWait) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		code, err := parseCallback(r.URL.RequestURI(), state, path)
		if errors.Is(err, errInvalidCallback) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Authorization failed.", http.StatusBadRequest)
			wait.send(callbackResult{err: err})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = io.WriteString(w, "<!doctype html><title>Authorization complete</title><p>You can close this tab.</p>")
		wait.send(callbackResult{code: code})
	})
}

func parseCallback(target, expectedState, prefix string) (string, error) {
	if strings.Contains(target, "#") || !strings.HasPrefix(target, prefix+"?") {
		return "", errInvalidCallback
	}
	q, err := url.ParseQuery(target[len(prefix)+1:])
	if err != nil {
		return "", errInvalidCallback
	}
	if q.Has("error") {
		state := q.Get("state")
		if state == "" || q.Get("error") == "" {
			return "", errInvalidCallback
		}
		if state != expectedState {
			return "", errStateMismatch
		}
		return "", errAccessDenied
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		return "", errInvalidCallback
	}
	if state != expectedState {
		return "", errStateMismatch
	}
	return code, nil
}

func authorizeURL(cfg providerConfig, authorize, redirect, challenge, state string) string {
	pairs := [][2]string{
		{"response_type", "code"},
		{"client_id", cfg.clientID},
		{"redirect_uri", redirect},
		{"scope", cfg.scope},
		{"code_challenge", challenge},
		{"code_challenge_method", "S256"},
	}
	switch cfg.name {
	case providerCodex:
		pairs = append(pairs,
			[2]string{"id_token_add_organizations", "true"},
			[2]string{"codex_cli_simplified_flow", "true"},
			[2]string{"state", state},
			[2]string{"originator", "fx"},
		)
	case providerGrok:
		pairs = append(pairs,
			[2]string{"state", state},
			[2]string{"referrer", "fx"},
		)
	}
	return authorize + "?" + formBody(pairs...)
}

func exchangeCode(ctx context.Context, cfg providerConfig, ep endpoint, code, verifier, redirect string) (Credential, error) {
	body := formBody(
		[2]string{"grant_type", "authorization_code"},
		[2]string{"client_id", cfg.clientID},
		[2]string{"code", code},
		[2]string{"code_verifier", verifier},
		[2]string{"redirect_uri", redirect},
	)
	raw, err := post(ctx, ep.token, "application/x-www-form-urlencoded", body)
	if err != nil {
		return Credential{}, err
	}
	access, refreshToken, expiresIn, hasExpiry, err := decodeToken(raw)
	if err != nil {
		return Credential{}, err
	}
	if refreshToken == "" || !hasExpiry {
		return Credential{}, errInvalidToken
	}
	account, err := accountID(ctx, cfg, ep, access)
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		Provider:     cfg.name,
		AccessToken:  access,
		RefreshToken: refreshToken,
		AccountID:    account,
		Expiry:       time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, nil
}

func refresh(ctx context.Context, cred Credential) (Credential, error) {
	cfg, ep, err := resolveEndpoint(cred.Provider)
	if err != nil {
		return Credential{}, err
	}
	var (
		body        string
		contentType string
	)
	if cfg.refreshJSON {
		payload, err := json.Marshal(struct {
			ClientID     string `json:"client_id"`
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
		}{
			ClientID:     cfg.clientID,
			GrantType:    "refresh_token",
			RefreshToken: cred.RefreshToken,
		})
		if err != nil {
			return Credential{}, err
		}
		body = string(payload)
		contentType = "application/json"
	} else {
		body = formBody(
			[2]string{"grant_type", "refresh_token"},
			[2]string{"client_id", cfg.clientID},
			[2]string{"refresh_token", cred.RefreshToken},
		)
		contentType = "application/x-www-form-urlencoded"
	}
	raw, err := post(ctx, ep.token, contentType, body)
	if err != nil {
		return Credential{}, err
	}
	access, rotated, expiresIn, hasExpiry, err := decodeToken(raw)
	if err != nil {
		return Credential{}, err
	}
	var expiry time.Time
	if hasExpiry {
		expiry = time.Now().Add(time.Duration(expiresIn) * time.Second)
	} else if cfg.name == providerCodex {
		expiry, err = jwtExpiry(access)
		if err != nil {
			return Credential{}, err
		}
	} else {
		return Credential{}, errInvalidToken
	}
	account, err := accountID(ctx, cfg, ep, access)
	if err != nil {
		return Credential{}, err
	}
	if cred.AccountID != "" && account != cred.AccountID {
		return Credential{}, errAccountChanged
	}
	next := cred
	next.AccessToken = access
	next.AccountID = account
	next.Expiry = expiry
	if rotated != "" {
		next.RefreshToken = rotated
	}
	return next, nil
}

func revoke(ctx context.Context, cred Credential) error {
	cfg, ep, err := resolveEndpoint(cred.Provider)
	if err != nil {
		return err
	}
	var body string
	if cfg.name == providerCodex {
		body = formBody(
			[2]string{"client_id", cfg.clientID},
			[2]string{"token", cred.RefreshToken},
			[2]string{"token_type_hint", "refresh_token"},
		)
	} else {
		body = formBody(
			[2]string{"token", cred.RefreshToken},
			[2]string{"client_id", cfg.clientID},
		)
	}
	_, err = post(ctx, ep.revoke, "application/x-www-form-urlencoded", body)
	return err
}

func accountID(ctx context.Context, cfg providerConfig, ep endpoint, access string) (string, error) {
	if cfg.name == providerGrok {
		return grokAccountID(ctx, ep.userinfo, access)
	}
	return codexAccountID(access)
}

func codexAccountID(token string) (string, error) {
	claims, err := jwtClaims(token)
	if err != nil {
		return "", err
	}
	raw, ok := claims[jwtAuthClaim]
	if !ok {
		return "", errInvalidToken
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", errInvalidToken
	}
	id, _ := obj["chatgpt_account_id"].(string)
	if !validAccountID(id) {
		return "", errInvalidToken
	}
	return id, nil
}

func grokAccountID(ctx context.Context, userinfo, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfo, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: userinfo %s", errInvalidToken, resp.Status)
	}
	var body struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", errInvalidToken
	}
	if !validAccountID(body.Sub) {
		return "", errInvalidToken
	}
	return body.Sub, nil
}

func decodeToken(data []byte) (access, refreshToken string, expiresIn int64, hasExpiry bool, err error) {
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    *int64 `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", "", 0, false, errInvalidToken
	}
	if raw.Error != "" {
		return "", "", 0, false, fmt.Errorf("%w: %s", errInvalidToken, raw.Error)
	}
	if raw.AccessToken == "" {
		return "", "", 0, false, errInvalidToken
	}
	if raw.ExpiresIn != nil {
		if *raw.ExpiresIn <= 0 {
			return "", "", 0, false, errInvalidToken
		}
		return raw.AccessToken, raw.RefreshToken, *raw.ExpiresIn, true, nil
	}
	return raw.AccessToken, raw.RefreshToken, 0, false, nil
}

func jwtExpiry(token string) (time.Time, error) {
	claims, err := jwtClaims(token)
	if err != nil {
		return time.Time{}, err
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, errInvalidToken
	}
	return time.Unix(int64(exp), 0), nil
}

func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, errInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, errInvalidToken
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil || claims == nil {
		return nil, errInvalidToken
	}
	return claims, nil
}

func post(ctx context.Context, endpoint, contentType, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth request failed: %s", resp.Status)
	}
	return data, nil
}

var httpClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func randomSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func formBody(pairs ...[2]string) string {
	var b strings.Builder
	for i, pair := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(percentEncode(pair[0]))
		b.WriteByte('=')
		b.WriteString(percentEncode(pair[1]))
	}
	return b.String()
}

func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
