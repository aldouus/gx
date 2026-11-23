package provider

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const grokVersionURL = "https://x.ai/cli/stable"

var (
	versionMu    sync.Mutex
	grokVersion  string
	grokLookedUp bool
)

func (r Responses) clientVersion(ctx context.Context) string {
	if r.Kind != "grok" {
		return ""
	}
	if r.ClientVersion != "" {
		return r.ClientVersion
	}
	return lookupGrokVersion(ctx)
}

// lookupGrokVersion reads the stable CLI version once per process.
// GX_TEST_GROK_VERSION_URL overrides the endpoint for tests. A failure leaves
// the header off rather than failing the model call.
func lookupGrokVersion(ctx context.Context) string {
	if override := os.Getenv("GX_TEST_GROK_VERSION_URL"); override != "" {
		return fetchGrokVersion(ctx, override)
	}
	versionMu.Lock()
	defer versionMu.Unlock()
	if grokLookedUp {
		return grokVersion
	}
	grokLookedUp = true
	grokVersion = fetchGrokVersion(ctx, grokVersionURL)
	return grokVersion
}

func fetchGrokVersion(ctx context.Context, url string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64))
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(b))
	if !validVersion(version) {
		return ""
	}
	return version
}

func validVersion(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	dot := false
	for _, r := range s {
		if r == '.' {
			dot = true
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return dot
}
