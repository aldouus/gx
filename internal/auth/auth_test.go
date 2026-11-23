package auth

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPKCEChallengeVector(t *testing.T) {
	// RFC 7636 appendix B.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := pkceChallenge(verifier); got != want {
		t.Fatalf("challenge %s", got)
	}
}

func TestParseCallbackStateMismatch(t *testing.T) {
	code, err := parseCallback("/auth/callback?code=auth%20code&state=expected", "expected", codexCallbackPath)
	if err != nil || code != "auth code" {
		t.Fatalf("code=%q err=%v", code, err)
	}
	code, err = parseCallback("/auth/callback?code=auth+code&state=expected", "expected", codexCallbackPath)
	if err != nil || code != "auth code" {
		t.Fatalf("plus code=%q err=%v", code, err)
	}
	_, err = parseCallback("/auth/callback?code=auth&state=other", "expected", codexCallbackPath)
	if !errors.Is(err, errStateMismatch) {
		t.Fatalf("codex mismatch: %v", err)
	}
	_, err = parseCallback("/other?code=auth&state=expected", "expected", codexCallbackPath)
	if !errors.Is(err, errInvalidCallback) {
		t.Fatalf("codex path: %v", err)
	}
	_, err = parseCallback("/auth/callback?code=auth&state=expected#frag", "expected", codexCallbackPath)
	if !errors.Is(err, errInvalidCallback) {
		t.Fatalf("fragment: %v", err)
	}

	code, err = parseCallback("/callback?code=auth%20code&state=expected", "expected", grokCallbackPath)
	if err != nil || code != "auth code" {
		t.Fatalf("grok code=%q err=%v", code, err)
	}
	_, err = parseCallback("/callback?code=auth&state=other", "expected", grokCallbackPath)
	if !errors.Is(err, errStateMismatch) {
		t.Fatalf("grok mismatch: %v", err)
	}
	_, err = parseCallback("/callback?error=access_denied&state=other", "expected", grokCallbackPath)
	if !errors.Is(err, errStateMismatch) {
		t.Fatalf("grok denial mismatch: %v", err)
	}
	_, err = parseCallback("/callback?error=access_denied&state=expected", "expected", grokCallbackPath)
	if !errors.Is(err, errAccessDenied) {
		t.Fatalf("grok denial: %v", err)
	}
}

func TestSaveLoadRoundTripMode(t *testing.T) {
	root := t.TempDir()
	cred := Credential{
		Provider:     providerCodex,
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		AccountID:    "acct_test",
		Expiry:       time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC),
	}
	if err := Save(root, cred); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "auth", "codex.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != filePerm {
		t.Fatalf("file mode %o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Join(root, "auth"))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != dirPerm {
		t.Fatalf("dir mode %o", dirInfo.Mode().Perm())
	}
	loaded, err := Load(root, providerCodex)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provider != cred.Provider || loaded.AccessToken != cred.AccessToken || loaded.RefreshToken != cred.RefreshToken || loaded.AccountID != cred.AccountID || !loaded.Expiry.Equal(cred.Expiry) {
		t.Fatalf("loaded %+v", loaded)
	}
	if loaded.root != root {
		t.Fatal("root was not kept on the loaded credential")
	}
	if _, err := Load(root, providerGrok); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing grok file: %v", err)
	}
}

func TestRefreshAgainstHTTPTest(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		var mu sync.Mutex
		var gotBody, gotType, gotPath string
		access := jwtWithClaims(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_test"},
			"exp":                         time.Now().Add(2 * time.Hour).Unix(),
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotBody = string(body)
			gotType = r.Header.Get("Content-Type")
			gotPath = r.URL.Path
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  access,
				"refresh_token": "rotated",
				"expires_in":    3600,
			})
		}))
		defer srv.Close()
		t.Setenv(envTestIssuer, srv.URL)

		root := t.TempDir()
		original := Credential{
			Provider:     providerCodex,
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			AccountID:    "acct_test",
			Expiry:       time.Now().Add(-time.Hour),
		}
		if err := Save(root, original); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(root, providerCodex)
		if err != nil {
			t.Fatal(err)
		}
		if got := Authorization(loaded); got != "Bearer "+access {
			t.Fatalf("authorization %q", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if gotPath != "/oauth/token" {
			t.Fatalf("path %s", gotPath)
		}
		if gotType != "application/json" {
			t.Fatalf("content-type %s", gotType)
		}
		wantBody := `{"client_id":"` + codexClientID + `","grant_type":"refresh_token","refresh_token":"old-refresh"}`
		if gotBody != wantBody {
			t.Fatalf("body %s", gotBody)
		}
		saved, err := Load(root, providerCodex)
		if err != nil {
			t.Fatal(err)
		}
		if saved.AccessToken != access || saved.RefreshToken != "rotated" || saved.AccountID != "acct_test" {
			t.Fatalf("saved %+v", saved)
		}
	})

	t.Run("grok", func(t *testing.T) {
		var mu sync.Mutex
		var tokenBody, tokenType, userAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			switch r.URL.Path {
			case "/oauth2/token":
				mu.Lock()
				tokenBody = string(body)
				tokenType = r.Header.Get("Content-Type")
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "new-access",
					"expires_in":   3600,
				})
			case "/oauth2/userinfo":
				mu.Lock()
				userAuth = r.Header.Get("Authorization")
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]string{"sub": "acct_test"})
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		t.Setenv(envTestIssuer, srv.URL)

		next, err := refresh(t.Context(), Credential{
			Provider:     providerGrok,
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			AccountID:    "acct_test",
			Expiry:       time.Now().Add(-time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if next.AccessToken != "new-access" || next.RefreshToken != "old-refresh" || next.AccountID != "acct_test" {
			t.Fatalf("refreshed %+v", next)
		}
		mu.Lock()
		defer mu.Unlock()
		if tokenType != "application/x-www-form-urlencoded" {
			t.Fatalf("content-type %s", tokenType)
		}
		wantBody := "grant_type=refresh_token&client_id=" + grokClientID + "&refresh_token=old-refresh"
		if tokenBody != wantBody {
			t.Fatalf("body %s", tokenBody)
		}
		if userAuth != "Bearer new-access" {
			t.Fatalf("userinfo auth %q", userAuth)
		}
	})

	t.Run("codex omits expiry and rotation", func(t *testing.T) {
		exp := time.Now().Add(2 * time.Hour).Unix()
		access := jwtWithClaims(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_test"},
			"exp":                         exp,
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": access})
		}))
		defer srv.Close()
		t.Setenv(envTestIssuer, srv.URL)
		next, err := refresh(t.Context(), Credential{
			Provider:     providerCodex,
			AccessToken:  "old",
			RefreshToken: "keep-me",
			AccountID:    "acct_test",
		})
		if err != nil {
			t.Fatal(err)
		}
		if next.RefreshToken != "keep-me" || next.AccessToken != access {
			t.Fatalf("refreshed %+v", next)
		}
		if next.Expiry.Unix() != exp {
			t.Fatalf("expiry %s", next.Expiry)
		}
	})
}

func TestLoginAgainstHTTPTest(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		access := jwtWithClaims(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_test"},
		})
		var mu sync.Mutex
		var form url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/token" || r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			body, _ := io.ReadAll(r.Body)
			parsed, _ := url.ParseQuery(string(body))
			mu.Lock()
			form = parsed
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  access,
				"refresh_token": "refresh-1",
				"expires_in":    3600,
			})
		}))
		defer srv.Close()
		t.Setenv(envTestIssuer, srv.URL)

		root := t.TempDir()
		cred, authURL := driveLogin(t, root, providerCodex, "auth-code")
		q := authURL.Query()
		if authURL.Scheme+"://"+authURL.Host != srv.URL || authURL.Path != "/oauth/authorize" {
			t.Fatalf("authorize %s", authURL)
		}
		if q.Get("response_type") != "code" || q.Get("client_id") != codexClientID || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("query %s", q)
		}
		if q.Get("id_token_add_organizations") != "true" || q.Get("codex_cli_simplified_flow") != "true" || q.Get("originator") != "gx" {
			t.Fatalf("extra %s", q)
		}
		if q.Get("scope") != codexScope {
			t.Fatalf("scope %q", q.Get("scope"))
		}
		redirect, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			t.Fatal(err)
		}
		if redirect.Hostname() != "localhost" || redirect.Path != codexCallbackPath {
			t.Fatalf("redirect %s", redirect)
		}
		mu.Lock()
		defer mu.Unlock()
		if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != codexClientID || form.Get("code") != "auth-code" || form.Get("redirect_uri") != q.Get("redirect_uri") {
			t.Fatalf("form %s", form.Encode())
		}
		if pkceChallenge(form.Get("code_verifier")) != q.Get("code_challenge") {
			t.Fatal("pkce verifier does not match the authorize challenge")
		}
		if cred.Provider != providerCodex || cred.AccessToken != access || cred.RefreshToken != "refresh-1" || cred.AccountID != "acct_test" {
			t.Fatalf("cred %+v", cred)
		}
		info, err := os.Stat(filepath.Join(root, "auth", "codex.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != filePerm {
			t.Fatalf("mode %o", info.Mode().Perm())
		}
	})

	t.Run("grok", func(t *testing.T) {
		var mu sync.Mutex
		var form url.Values
		var userAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			switch r.URL.Path {
			case "/oauth2/token":
				parsed, _ := url.ParseQuery(string(body))
				mu.Lock()
				form = parsed
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "grok-access",
					"refresh_token": "grok-refresh",
					"expires_in":    1800,
				})
			case "/oauth2/userinfo":
				mu.Lock()
				userAuth = r.Header.Get("Authorization")
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]string{"sub": "acct_grok"})
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		t.Setenv(envTestIssuer, srv.URL)

		cred, authURL := driveLogin(t, t.TempDir(), providerGrok, "grok-code")
		q := authURL.Query()
		if authURL.Path != "/oauth2/authorize" || q.Get("referrer") != "gx" || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("authorize %s", authURL)
		}
		if q.Get("client_id") != grokClientID || q.Get("scope") != grokScope {
			t.Fatalf("query %s", q)
		}
		redirect, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			t.Fatal(err)
		}
		if redirect.Hostname() != "127.0.0.1" || redirect.Path != grokCallbackPath {
			t.Fatalf("redirect %s", redirect)
		}
		mu.Lock()
		defer mu.Unlock()
		if form.Get("code") != "grok-code" || form.Get("grant_type") != "authorization_code" || pkceChallenge(form.Get("code_verifier")) != q.Get("code_challenge") {
			t.Fatalf("form %s", form.Encode())
		}
		if userAuth != "Bearer grok-access" {
			t.Fatalf("userinfo %q", userAuth)
		}
		if cred.AccountID != "acct_grok" || cred.RefreshToken != "grok-refresh" || cred.AccessToken != "grok-access" {
			t.Fatalf("cred %+v", cred)
		}
	})
}

func TestLoginHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	}))
	defer srv.Close()
	t.Setenv(envTestIssuer, srv.URL)

	ctx, cancel := context.WithCancel(t.Context())
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		_, err := Login(ctx, t.TempDir(), providerCodex, pw)
		_ = pw.Close()
		errCh <- err
	}()
	_ = readAuthURL(t, pr)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := Login(canceled, t.TempDir(), providerCodex, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	t.Setenv(envTestIssuer, "https://127.0.0.1:1/token")
	if _, err := Login(t.Context(), t.TempDir(), providerCodex, io.Discard); !errors.Is(err, errInvalidTestIssuer) {
		t.Fatal(err)
	}
	t.Setenv(envTestIssuer, "http://example.com:1/token")
	if _, err := Login(t.Context(), t.TempDir(), providerGrok, io.Discard); !errors.Is(err, errInvalidTestIssuer) {
		t.Fatal(err)
	}
}

func TestLogoutRevokesThenDeletes(t *testing.T) {
	var mu sync.Mutex
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		parsed, _ := url.ParseQuery(string(body))
		mu.Lock()
		got = parsed
		mu.Unlock()
		if r.URL.Path != "/oauth/revoke" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv(envTestIssuer, srv.URL)

	root := t.TempDir()
	cred := Credential{
		Provider:     providerCodex,
		AccessToken:  "access",
		RefreshToken: "refresh-me",
		AccountID:    "acct_test",
		Expiry:       time.Now().Add(time.Hour),
	}
	if err := Save(root, cred); err != nil {
		t.Fatal(err)
	}
	if err := Logout(t.Context(), root, providerCodex); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if got.Get("token") != "refresh-me" || got.Get("client_id") != codexClientID || got.Get("token_type_hint") != "refresh_token" {
		t.Fatalf("revoke %s", got.Encode())
	}
	mu.Unlock()
	if _, err := Load(root, providerCodex); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
	if err := Logout(t.Context(), root, providerCodex); err != nil {
		t.Fatal(err)
	}

	if err := Save(root, cred); err != nil {
		t.Fatal(err)
	}
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer fail.Close()
	t.Setenv(envTestIssuer, fail.URL)
	if err := Logout(t.Context(), root, providerCodex); err == nil {
		t.Fatal("expected revoke failure")
	}
	if _, err := Load(root, providerCodex); err != nil {
		t.Fatalf("credential deleted after failed revoke: %v", err)
	}
}

func TestAuthorizationKeepsFreshToken(t *testing.T) {
	cred := Credential{Provider: providerCodex, AccessToken: "still-good", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	if got := Authorization(cred); got != "Bearer still-good" {
		t.Fatal(got)
	}
}

func driveLogin(t *testing.T, root, provider, code string) (Credential, *url.URL) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	type result struct {
		cred Credential
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		cred, err := Login(ctx, root, provider, pw)
		_ = pw.Close()
		ch <- result{cred, err}
	}()
	raw := readAuthURL(t, pr)
	go io.Copy(io.Discard, pr)
	hitCallback(t, raw, code)
	got := <-ch
	_, _ = io.Copy(io.Discard, pr)
	if got.err != nil {
		t.Fatal(got.err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return got.cred, u
}

func readAuthURL(t *testing.T, r io.Reader) string {
	t.Helper()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if url, ok := oscLink(trimmed); ok {
			return url
		}
		if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
			return trimmed
		}
		if err != nil {
			t.Fatalf("read authorize url: %v", err)
		}
	}
}

func oscLink(line string) (string, bool) {
	const open = "\x1b]8;;"
	i := strings.Index(line, open)
	if i < 0 {
		return "", false
	}
	rest := line[i+len(open):]
	url, _, ok := strings.Cut(rest, "\x1b\\")
	if !ok || !strings.HasPrefix(url, "http") {
		return "", false
	}
	return url, true
}

func hitCallback(t *testing.T, authURL, code string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Port() == "" || q.Get("state") == "" {
		t.Fatalf("authorize url %s", authURL)
	}
	callback := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", redirect.Port()),
		Path:   redirect.Path,
	}
	cq := url.Values{}
	cq.Set("code", code)
	cq.Set("state", q.Get("state"))
	callback.RawQuery = cq.Encode()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback %d %s", resp.StatusCode, body)
	}
}

func jwtWithClaims(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln"
}
