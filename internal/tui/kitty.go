package tui

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var imageIDs struct {
	sync.Mutex
	n int
}

// ShowImage draws a local image inline with the Kitty graphics protocol and
// leaves the cursor below it. Terminals that ignore the sequence still keep
// the filename link.
func (t *Transcript) ShowImage(path string) error {
	seq, rows, err := KittyImage(path, t.cols)
	if err != nil {
		return t.User(ImageBadge(1, path))
	}
	var b strings.Builder
	b.WriteString(seq)
	for i := 0; i < rows; i++ {
		b.WriteString("\r\n")
	}
	b.WriteString(fileLink(filepathBase(path), path))
	b.WriteString("\r\n")
	t.screen.Reset()
	return t.screen.WriteRaw(b.String())
}

// KittyImage is an APC transmit-and-display sequence for a PNG, plus the
// number of rows the placement occupies.
func KittyImage(path string, termCols int) (string, int, error) {
	data, w, h, err := pngBytes(path)
	if err != nil {
		return "", 0, err
	}
	if len(data) == 0 || len(data) > 20<<20 {
		return "", 0, fmt.Errorf("image is empty or too large")
	}
	cellW, cellH := cellPixels(termCols)
	cols, rows := fitImage(w, h, termCols, cellW, cellH)
	return kittyPNG(data, nextImageID(), cols, rows), rows, nil
}

func kittyPNG(data []byte, id, cols, rows int) string {
	payload := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	const chunk = 4096
	for i := 0; i < len(payload) || i == 0; i += chunk {
		end := i + chunk
		if end > len(payload) {
			end = len(payload)
		}
		more := 0
		if end < len(payload) {
			more = 1
		}
		b.WriteString("\x1b_G")
		if i == 0 {
			fmt.Fprintf(&b, "a=T,f=100,t=d,q=2,i=%d,c=%d,r=%d,m=%d", id, cols, rows, more)
		} else {
			fmt.Fprintf(&b, "m=%d", more)
		}
		b.WriteByte(';')
		if end > i {
			b.WriteString(payload[i:end])
		}
		b.WriteString("\x1b\\")
		if end == len(payload) {
			break
		}
	}
	return b.String()
}

func nextImageID() int {
	imageIDs.Lock()
	defer imageIDs.Unlock()
	imageIDs.n++
	if imageIDs.n > 4294967295 {
		imageIDs.n = 1
	}
	return imageIDs.n
}

func pngBytes(path string) ([]byte, int, int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	if w, h, ok := pngSize(b); ok {
		return b, w, h, nil
	}
	tmp, err := os.CreateTemp("", "gx-image-*.png")
	if err != nil {
		return nil, 0, 0, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.Command("sips", "-s", "format", "png", path, "--out", tmp.Name())
	if err := cmd.Run(); err != nil {
		return nil, 0, 0, fmt.Errorf("cannot display %s", filepath.Base(path))
	}
	png, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, 0, 0, err
	}
	w, h, ok := pngSize(png)
	if !ok {
		return nil, 0, 0, fmt.Errorf("cannot display %s", filepath.Base(path))
	}
	return png, w, h, nil
}

func pngSize(b []byte) (int, int, bool) {
	if len(b) < 24 || string(b[:8]) != "\x89PNG\r\n\x1a\n" {
		return 0, 0, false
	}
	w := int(binary.BigEndian.Uint32(b[16:20]))
	h := int(binary.BigEndian.Uint32(b[20:24]))
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func cellPixels(termCols int) (int, int) {
	x, y := pixelSize(StdoutFD())
	_, cols := size(StdoutFD())
	rows, _ := size(StdoutFD())
	if cols < 1 {
		cols = termCols
	}
	if x > 0 && y > 0 && cols > 0 && rows > 0 {
		cw := x / cols
		ch := y / rows
		if cw > 0 && ch > 0 {
			return cw, ch
		}
	}
	return 8, 16
}

func fitImage(imgW, imgH, termCols, cellW, cellH int) (int, int) {
	if cellW < 1 {
		cellW = 8
	}
	if cellH < 1 {
		cellH = 16
	}
	maxCols := termCols - 4
	if maxCols < 8 {
		maxCols = 8
	}
	if maxCols > 60 {
		maxCols = 60
	}
	cols := (imgW + cellW - 1) / cellW
	if cols > maxCols {
		cols = maxCols
	}
	if cols < 1 {
		cols = 1
	}
	rows := (imgH*cols*cellW + imgW*cellH - 1) / (imgW * cellH)
	if rows < 1 {
		rows = 1
	}
	if rows > 24 {
		rows = 24
	}
	return cols, rows
}
