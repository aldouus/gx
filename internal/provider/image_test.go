package provider

import (
	"encoding/binary"
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

func TestImageDimensionCap(t *testing.T) {
	dir := t.TempDir()
	okPath := filepath.Join(dir, "ok.png")
	bigPath := filepath.Join(dir, "big.png")
	if err := os.WriteFile(okPath, pngOf(8, 8), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FileDataURL(okPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bigPath, pngOf(8001, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FileDataURL(bigPath); err == nil || !strings.Contains(err.Error(), "8000") {
		t.Fatal(err)
	}
	if MaxDimension(21) != 2000 || MaxDimension(1) != 8000 {
		t.Fatal(MaxDimension(21), MaxDimension(1))
	}
}

func pngOf(w, h int) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	b = append(b, 0, 0, 0, 13)
	b = append(b, 'I', 'H', 'D', 'R')
	var dim [8]byte
	binary.BigEndian.PutUint32(dim[0:4], uint32(w))
	binary.BigEndian.PutUint32(dim[4:8], uint32(h))
	return append(b, dim[:]...)
}
