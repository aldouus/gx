// Package auth stores Codex and Grok browser-login credentials under a gx home
// directory and refreshes them. It never reads or writes ~/.fx.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	providerCodex = "codex"
	providerGrok  = "grok"

	dirPerm  = 0o700
	filePerm = 0o600

	// maxCredBytes matches the fx session file cap.
	maxCredBytes = 64 * 1024
)

// Credential is a saved Codex ("codex") or Grok ("grok") OAuth login.
type Credential struct {
	Provider     string    `json:"provider"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	AccountID    string    `json:"account_id"`
	Expiry       time.Time `json:"expiry"`

	// root is the gx home passed to Load, Save, or Login. Authorization uses it
	// to persist a refresh. It is not written to disk.
	root string
}

// Load reads {root}/auth/{provider}.json.
func Load(root, provider string) (Credential, error) {
	path, err := credPath(root, provider)
	if err != nil {
		return Credential{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Credential{}, err
	}
	if len(data) > maxCredBytes {
		return Credential{}, fmt.Errorf("auth: credential file too large")
	}
	var cred Credential
	if err := json.Unmarshal(data, &cred); err != nil {
		return Credential{}, fmt.Errorf("auth: credential file: %w", err)
	}
	if cred.Provider != provider {
		return Credential{}, fmt.Errorf("auth: credential provider %q", cred.Provider)
	}
	if cred.AccessToken == "" {
		return Credential{}, errors.New("auth: empty access token")
	}
	cred.root = root
	return cred, nil
}

// Save writes {root}/auth/{provider}.json with mode 0600. The auth directory
// is created with mode 0700.
func Save(root string, cred Credential) error {
	if err := validate(cred); err != nil {
		return err
	}
	path, err := credPath(root, cred.Provider)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stored(cred), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writePrivate(path, data)
}

// Delete removes {root}/auth/{provider}.json.
func Delete(root, provider string) error {
	path, err := credPath(root, provider)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func stored(cred Credential) Credential {
	cred.root = ""
	return cred
}

func validate(cred Credential) error {
	if _, err := lookupProvider(cred.Provider); err != nil {
		return err
	}
	if cred.AccessToken == "" {
		return errors.New("auth: empty access token")
	}
	if cred.AccountID != "" && !validAccountID(cred.AccountID) {
		return errors.New("auth: invalid account id")
	}
	return nil
}

func credPath(root, provider string) (string, error) {
	if root == "" {
		return "", errors.New("auth: empty root")
	}
	if _, err := lookupProvider(provider); err != nil {
		return "", err
	}
	return filepath.Join(root, "auth", provider+".json"), nil
}

func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(filePerm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	if err := os.Chmod(path, filePerm); err != nil {
		return err
	}
	return nil
}

func validAccountID(id string) bool {
	if id == "" || len(id) > 1024 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}
