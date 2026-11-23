package skills

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CatalogBudget is how much of the skill list is injected into the prompt.
const CatalogBudget = 16 << 10

// Catalog formats the discovered skills, stopping at CatalogBudget bytes.
func Catalog(found []Skill) string {
	var b strings.Builder
	b.WriteString("Skills available. Load a skill before following it.\n")
	for _, sk := range found {
		line := fmt.Sprintf("- %s: %s (%s)\n", sk.Name, sk.Description, sk.Location)
		if b.Len()+len(line) > CatalogBudget {
			b.WriteString("- …\n")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// Show returns one skill's location and instructions.
func Show(found []Skill, name string) (string, error) {
	for _, sk := range found {
		if sk.Name != name {
			continue
		}
		body, err := Load(sk.Path)
		if err != nil {
			return "", err
		}
		return sk.Location + "\n" + body, nil
	}
	return "", fmt.Errorf("no skill %s", name)
}

// Create writes a skill directory under root.
func Create(root, name, description string) (string, error) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return "", fmt.Errorf("skill name %q is not a single path element", name)
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if description == "" {
		description = name
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	path := filepath.Join(dir, skillFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Remove deletes a skill directory by name under root.
func Remove(root, name string) error {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return fmt.Errorf("skill name %q is not a single path element", name)
	}
	return os.RemoveAll(filepath.Join(root, name))
}

// Install copies a skill directory, or downloads an http(s) SKILL.md, into root.
func Install(src, root string) (string, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return installURL(src, root)
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		src = filepath.Dir(src)
	}
	name := filepath.Base(src)
	dst := filepath.Join(root, name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	if err := copyTree(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func installURL(raw, root string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("skill install: %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	name, description, _ := parseSkill(string(body), "")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(req.URL.Path), ".md")
	}
	if name == "" || name == "/" || name == "." {
		return "", fmt.Errorf("skill install: no name in %s", raw)
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, skillFileName)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	if description == "" && !strings.Contains(string(body), "description:") {
		return path, nil
	}
	return path, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
