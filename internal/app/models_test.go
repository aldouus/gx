package app

import "testing"

func TestGrokCatalogKeepsModelsOutOfEfforts(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"grok-4.7","model":"grok-4.7","api_backend":"responses","supports_reasoning_effort":true,"reasoning_efforts":[{"id":"xhigh","value":"xhigh"},{"id":"high","value":"high"},{"id":"medium","value":"medium"},{"id":"low","value":"low"}]},
		{"id":"grok-4.7-build-fast","model":"grok-4.7-build-fast","api_backend":"responses","reasoning_efforts":[{"value":"high"}]},
		{"id":"chat-only","model":"chat-only","api_backend":"chat_completions","reasoning_efforts":[]},
		{"id":"grok-4.5","model":"grok-4.5","api_backend":"responses","reasoning_efforts":[{"value":"low"}]}
	]}`)
	models := parseGrokCatalog(body)
	ids := modelIDs(models)
	if len(ids) != 2 || ids[0] != "grok-4.7" || ids[1] != "grok-4.5" {
		t.Fatalf("models %v", ids)
	}
	efforts := effortsFor(models, "grok-4.7")
	if len(efforts) != 4 || efforts[0] != "xhigh" || efforts[3] != "low" {
		t.Fatalf("efforts %v", efforts)
	}
}

func TestCodexCatalogSkipsHiddenModels(t *testing.T) {
	body := []byte(`{"models":[
		{"slug":"gpt-5.4","visibility":"list","supported_in_api":true,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		{"slug":"hidden","visibility":"hide","supported_in_api":true,"supported_reasoning_levels":[{"effort":"low"}]},
		{"slug":"unsupported","visibility":"list","supported_in_api":false,"supported_reasoning_levels":[]},
		{"slug":"gpt-5.4-fast","visibility":"list","supported_in_api":true,"supported_reasoning_levels":[{"effort":"low"}]}
	]}`)
	models := parseCodexCatalog(body)
	ids := modelIDs(models)
	if len(ids) != 1 || ids[0] != "gpt-5.4" {
		t.Fatalf("models %v", ids)
	}
	if got := effortsFor(models, "gpt-5.4"); len(got) != 2 || got[0] != "low" || got[1] != "high" {
		t.Fatalf("efforts %v", got)
	}
}
