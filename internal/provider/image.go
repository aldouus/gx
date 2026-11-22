package provider

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
)

const (
	maxImageBytes   = 20 << 20
	maxImageDataURL = 5 << 20
)

// FileDataURL reads an image file and returns a data URL. Files over 20 MiB,
// and encodings over 5 MiB, are refused.
func FileDataURL(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", fmt.Errorf("image %s is empty", path)
	}
	if len(b) > maxImageBytes {
		return "", fmt.Errorf("image %s is larger than 20 MiB", path)
	}
	mime := http.DetectContentType(b)
	encoded := base64.StdEncoding.EncodeToString(b)
	if len(encoded) > maxImageDataURL {
		return "", fmt.Errorf("image %s encodes larger than 5 MiB", path)
	}
	return "data:" + mime + ";base64," + encoded, nil
}
