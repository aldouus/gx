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

// catalogModel is one picker row. Efforts are the reasoning levels that
// model offers. They are not models, and a fast variant is not a row.
type catalogModel struct {
	ID      string
	Efforts []string
}

func listProviderModels(home string, inv cli.Invocation, settings config.File) ([]catalogModel, error) {
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

func fetchModels(ctx context.Context, name string, cred auth.Credential) ([]catalogModel, error) {
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
	if name == "grok" {
		return parseGrokCatalog(body), nil
	}
	return parseCodexCatalog(body), nil
}

func parseGrokCatalog(body []byte) []catalogModel {
	var root struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	var models []catalogModel
	seen := map[string]bool{}
	for _, object := range root.Data {
		if stringField(object, "api_backend") != "responses" {
			continue
		}
		id := stringField(object, "model")
		if id == "" || seen[id] || fastVariant(id) {
			continue
		}
		seen[id] = true
		models = append(models, catalogModel{ID: id, Efforts: grokEfforts(object)})
	}
	return models
}

func parseCodexCatalog(body []byte) []catalogModel {
	var root struct {
		Models []map[string]any `json:"models"`
	}
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	var models []catalogModel
	seen := map[string]bool{}
	for _, object := range root.Models {
		if stringField(object, "visibility") != "list" || !boolField(object, "supported_in_api") {
			continue
		}
		id := stringField(object, "slug")
		if id == "" || seen[id] || fastVariant(id) {
			continue
		}
		seen[id] = true
		models = append(models, catalogModel{ID: id, Efforts: codexEfforts(object)})
	}
	return models
}

func grokEfforts(object map[string]any) []string {
	raw, _ := object["reasoning_efforts"].([]any)
	var efforts []string
	seen := map[string]bool{}
	for _, item := range raw {
		entry, _ := item.(map[string]any)
		value := stringField(entry, "value")
		if value == "" || seen[value] || value == "default" || value == "auto" {
			continue
		}
		seen[value] = true
		efforts = append(efforts, value)
	}
	return efforts
}

func codexEfforts(object map[string]any) []string {
	raw, _ := object["supported_reasoning_levels"].([]any)
	var efforts []string
	seen := map[string]bool{}
	for _, item := range raw {
		entry, _ := item.(map[string]any)
		value := stringField(entry, "effort")
		if value == "" || seen[value] || value == "default" || value == "auto" {
			continue
		}
		seen[value] = true
		efforts = append(efforts, value)
	}
	return efforts
}

func fastVariant(id string) bool {
	return strings.HasSuffix(id, "-fast") || strings.Contains(id, "-build-fast")
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func boolField(object map[string]any, key string) bool {
	value, _ := object[key].(bool)
	return value
}

func modelIDs(models []catalogModel) []string {
	ids := make([]string, len(models))
	for i, model := range models {
		ids[i] = model.ID
	}
	return ids
}

func effortsFor(models []catalogModel, id string) []string {
	for _, model := range models {
		if model.ID == id {
			return model.Efforts
		}
	}
	return nil
}
