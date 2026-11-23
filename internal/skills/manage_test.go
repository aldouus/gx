package skills

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("---\nname: remote\ndescription: from the network\n---\n\nDo the thing.\n"))
	}))
	defer srv.Close()
	root := t.TempDir()
	path, err := Install(srv.URL+"/remote.md", root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Do the thing.") {
		t.Fatal(string(body))
	}
	if filepath.Base(filepath.Dir(path)) != "remote" {
		t.Fatal(path)
	}
}
