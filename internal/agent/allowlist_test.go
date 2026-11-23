package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllowlistPersistsBothScopes(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	if err := AddGrant(ScopeGlobal, home, ws, "web_fetch", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	if err := AddGrant(ScopeWorkspace, home, ws, "shell", "git status"); err != nil {
		t.Fatal(err)
	}
	if err := AddGrant(ScopeWorkspace, home, ws, "read_file", "*"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(GlobalAllowlist(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("global mode %o", info.Mode().Perm())
	}
	info, err = os.Stat(WorkspaceAllowlist(ws))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("workspace mode %o", info.Mode().Perm())
	}
	if !FilesAllow(home, ws, "shell", "git status") {
		t.Fatal("workspace grant was not consulted")
	}
	if !FilesAllow(home, ws, "read_file", "/tmp/x") {
		t.Fatal("star grant did not cover the path")
	}
	if !FilesAllow(home, ws, "web_fetch", "https://example.com") {
		t.Fatal("global grant was not consulted")
	}
	if FilesAllow(home, ws, "shell", "rm -rf") {
		t.Fatal("unrelated command was allowed")
	}
	list, err := ListGrants(ScopeEffective, home, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("list %#v", list)
	}
	if err := RemoveGrant(ScopeWorkspace, home, ws, "shell", "git status"); err != nil {
		t.Fatal(err)
	}
	if FilesAllow(home, ws, "shell", "git status") {
		t.Fatal("removed grant still allowed")
	}
	if err := ResetGrants(ScopeGlobal, home, ws); err != nil {
		t.Fatal(err)
	}
	if FilesAllow(home, ws, "web_fetch", "https://example.com") {
		t.Fatal("reset global grant still allowed")
	}
	if !FilesAllow(home, ws, "read_file", "/tmp/x") {
		t.Fatal("workspace grant was cleared by a global reset")
	}
	if _, err := os.Stat(filepath.Join(home, "allowlist.json")); err != nil {
		t.Fatal(err)
	}
}
