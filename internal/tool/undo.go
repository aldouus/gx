package tool

import (
	"os"
	"sync"
)

type undoStep struct {
	path    string
	body    []byte
	existed bool
	mode    os.FileMode
}

var (
	undoMu    sync.Mutex
	undoStack []undoStep
)

func rememberUndo(path string) {
	info, err := os.Stat(path)
	step := undoStep{path: path, mode: 0o644}
	if err == nil && !info.IsDir() {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return
		}
		step.body = body
		step.existed = true
		step.mode = info.Mode().Perm()
	} else if err != nil && !os.IsNotExist(err) {
		return
	}
	undoMu.Lock()
	undoStack = append(undoStack, step)
	undoMu.Unlock()
}

// Undo restores the latest tracked write or edit. A missing step is an error
// string, not a panic.
func Undo() (string, error) {
	undoMu.Lock()
	if len(undoStack) == 0 {
		undoMu.Unlock()
		return "", os.ErrNotExist
	}
	step := undoStack[len(undoStack)-1]
	undoStack = undoStack[:len(undoStack)-1]
	undoMu.Unlock()
	if !step.existed {
		if err := os.Remove(step.path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "removed " + step.path, nil
	}
	if err := os.WriteFile(step.path, step.body, step.mode); err != nil {
		return "", err
	}
	return "restored " + step.path, nil
}
