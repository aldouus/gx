package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileDataURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dot.png")
	// Minimal PNG signature is enough for the content sniffer.
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	url, err := FileDataURL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatal(url)
	}
	body := encodeResponses(Request{Messages: []Message{{
		Role:  "user",
		Parts: []Part{{Text: "see"}, {ImageURL: url}},
	}}})
	raw, _ := body["input"].([]any)
	if len(raw) != 1 {
		t.Fatalf("input %#v", body["input"])
	}
	item := raw[0].(map[string]any)
	content := item["content"].([]any)
	if content[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("content %#v", content)
	}
}
