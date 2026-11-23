package provider

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
)

const (
	maxImageBytes     = 20 << 20
	maxImageDataURL   = 5 << 20
	maxImageDimension = 8000
	strictImageCount  = 20
	strictImageSide   = 2000
)

// FileDataURL reads an image file and returns a data URL. Files over 20 MiB,
// encodings over 5 MiB, and images over 8000 px on a side are refused.
func FileDataURL(path string) (string, error) {
	return fileDataURL(path, maxImageDimension)
}

// FileDataURLLimit is FileDataURL with a caller-chosen pixel cap. A request
// with more than 20 images uses 2000.
func FileDataURLLimit(path string, maxDim int) (string, error) {
	if maxDim <= 0 {
		maxDim = maxImageDimension
	}
	return fileDataURL(path, maxDim)
}

// MaxDimension is 8000, or 2000 when a request carries more than 20 images.
func MaxDimension(imageCount int) int {
	if imageCount > strictImageCount {
		return strictImageSide
	}
	return maxImageDimension
}

func fileDataURL(path string, maxDim int) (string, error) {
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
	if w, h, ok := imageSize(b); ok && (w > maxDim || h > maxDim) {
		return "", fmt.Errorf("image %s is larger than %d px", path, maxDim)
	}
	mime := http.DetectContentType(b)
	encoded := base64.StdEncoding.EncodeToString(b)
	if len(encoded) > maxImageDataURL {
		return "", fmt.Errorf("image %s encodes larger than 5 MiB", path)
	}
	return "data:" + mime + ";base64," + encoded, nil
}

func imageSize(b []byte) (int, int, bool) {
	switch {
	case len(b) >= 24 && string(b[:8]) == "\x89PNG\r\n\x1a\n" && string(b[12:16]) == "IHDR":
		return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), true
	case len(b) >= 10 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return int(binary.LittleEndian.Uint16(b[6:8])), int(binary.LittleEndian.Uint16(b[8:10])), true
	case len(b) >= 4 && b[0] == 0xff && b[1] == 0xd8:
		return jpegSize(b)
	case len(b) >= 16 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return webpSize(b)
	default:
		return 0, 0, false
	}
}

func jpegSize(b []byte) (int, int, bool) {
	i := 2
	for i+8 < len(b) {
		if b[i] != 0xff {
			return 0, 0, false
		}
		for i < len(b) && b[i] == 0xff {
			i++
		}
		if i >= len(b) {
			return 0, 0, false
		}
		marker := b[i]
		i++
		if marker == 0xd8 || marker == 0xd9 || (marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if i+1 >= len(b) {
			return 0, 0, false
		}
		seg := int(binary.BigEndian.Uint16(b[i : i+2]))
		if seg < 2 || i+seg > len(b) {
			return 0, 0, false
		}
		sof := marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc
		if sof && seg >= 7 {
			h := int(binary.BigEndian.Uint16(b[i+3 : i+5]))
			w := int(binary.BigEndian.Uint16(b[i+5 : i+7]))
			return w, h, w > 0 && h > 0
		}
		i += seg
	}
	return 0, 0, false
}

func webpSize(b []byte) (int, int, bool) {
	if len(b) >= 30 && string(b[12:16]) == "VP8X" {
		w := 1 + int(b[24]) + int(b[25])<<8 + int(b[26])<<16
		h := 1 + int(b[27]) + int(b[28])<<8 + int(b[29])<<16
		return w, h, true
	}
	return 0, 0, false
}
