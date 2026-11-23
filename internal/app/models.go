package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gx/internal/auth"
	"gx/internal/cli"
	"gx/internal/config"
)

func listProviderModels(home string, inv cli.Invocation, settings config.File) ([]string, error) {
	name := providerName(inv, settings)
	if name == "" || name == "openai" {
		for _, candidate := range []string{"grok", "codex"} {
			if cred, err := auth.Load(home, candidate); err == nil && cred.AccessToken != "" {
				name = candidate
				break
			}
		}
	}
	if name != "grok" && name != "codex" {
		return nil, fmt.Errorf("no subscription login. Run /login")
	}
	cred, err := auth.Load(home, name)
	if err != nil || cred.AccessToken == "" {
		return nil, fmt.Errorf("no %s login. Run /login", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return fetchModels(ctx, name, cred)
}

func fetchModels(ctx context.Context, name string, cred auth.Credential) ([]string, error) {
	url := "https://chatgpt.com/backend-api/codex/models"
	if name == "grok" {
		url = "https://cli-chat-proxy.grok.com/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	if name == "grok" {
		req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		req.Header.Set("x-grok-client-identifier", "fx")
		if cred.AccountID != "" {
			req.Header.Set("x-grok-user-id", cred.AccountID)
		}
	} else {
		req.Header.Set("originator", "fx")
		if cred.AccountID != "" {
			req.Header.Set("chatgpt-account-id", cred.AccountID)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("models status %d", res.StatusCode)
	}
	return modelIDs(body), nil
}

func modelIDs(body []byte) []string {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	var ids []string
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			if id, ok := n["id"].(string); ok && id != "" && !seen[id] && !strings.Contains(id, " ") {
				seen[id] = true
				ids = append(ids, id)
			}
			if slug, ok := n["slug"].(string); ok && slug != "" && !seen[slug] {
				seen[slug] = true
				ids = append(ids, slug)
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(root)
	return ids
}
