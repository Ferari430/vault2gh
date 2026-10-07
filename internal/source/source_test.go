package source

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vault.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(content))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestOpenZipFindsVaultRoot(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"wrapped in folder": {"MyVault/a.md": "a", "MyVault/img/p.png": "p", "__MACOSX/MyVault/._a.md": "x"},
		"files at root":     {"a.md": "a", "img/p.png": "p"},
		"obsidian marker":   {"V/.obsidian/app.json": "{}", "V/a.md": "a", "V/img/p.png": "p"},
	} {
		t.Run(name, func(t *testing.T) {
			root, cleanup, err := Open(makeZip(t, files))
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if _, err := os.Stat(filepath.Join(root, "a.md")); err != nil {
				t.Errorf("a.md not at vault root %s: %v", root, err)
			}
			if _, err := os.Stat(filepath.Join(root, "img", "p.png")); err != nil {
				t.Errorf("img/p.png missing: %v", err)
			}
		})
	}
}

func TestOpenCleanupRemovesTemp(t *testing.T) {
	root, cleanup, err := Open(makeZip(t, map[string]string{"a.md": "a"}))
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("temp dir still exists: %v", err)
	}
}

func TestUnzipRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../evil.md", "/etc/evil", `..\evil.md`, "a/../../evil.md"} {
		err := Unzip(makeZip(t, map[string]string{name: "x"}), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "недопустимый путь") {
			t.Errorf("%q: expected traversal error, got %v", name, err)
		}
	}
}

func TestUnzipSizeLimit(t *testing.T) {
	old := maxUnpacked
	maxUnpacked = 10
	defer func() { maxUnpacked = old }()

	err := Unzip(makeZip(t, map[string]string{"a.md": "123456", "b.md": "123456"}), t.TempDir())
	if !errors.Is(err, errTooBig) {
		t.Errorf("expected errTooBig, got %v", err)
	}
}

func TestOpenRejectsOtherFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notes.rar")
	os.WriteFile(p, []byte("x"), 0o644)
	if _, _, err := Open(p); err == nil {
		t.Error("expected error for .rar")
	}
}
