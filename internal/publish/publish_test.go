package publish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitHub имитирует API: репозиторий существует, только если создан через API.
// clone_url указывает на локальный bare-репозиторий.
type fakeGitHub struct {
	t       *testing.T
	remote  string
	created bool
	body    map[string]any
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer good" {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
		return
	}
	repo := map[string]any{
		"full_name": "alice/notes", "html_url": "https://github.com/alice/notes",
		"clone_url": f.remote, "default_branch": "main", "private": true,
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/user":
		json.NewEncoder(w).Encode(map[string]any{"login": "alice", "id": 7})
	case r.Method == "GET" && r.URL.Path == "/repos/alice/notes":
		if !f.created {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(repo)
	case r.Method == "POST" && r.URL.Path == "/user/repos":
		json.NewDecoder(r.Body).Decode(&f.body)
		f.created = true
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(repo)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestPublishEndToEnd(t *testing.T) {
	remote := t.TempDir()
	run(t, remote, "init", "-q", "--bare", "-b", "main")
	fake := &fakeGitHub{t: t, remote: remote}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	vaultDir := t.TempDir()
	os.MkdirAll(filepath.Join(vaultDir, "Daily"), 0o755)
	os.MkdirAll(filepath.Join(vaultDir, "Files"), 0o755)
	os.WriteFile(filepath.Join(vaultDir, "Daily", "today.md"), []byte("![[shot.png]]\n[[missing]]\n"), 0o644)
	os.WriteFile(filepath.Join(vaultDir, "Files", "shot.png"), []byte("png"), 0o644)

	var steps []string
	p := &Publisher{APIBaseURL: srv.URL, Progress: func(s string) { steps = append(steps, s) }}
	res, err := p.Publish(context.Background(), Request{Source: vaultDir, Token: "good", Repo: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || !res.Changed || res.Branch != "main" {
		t.Errorf("result: %+v", res)
	}
	if fake.body["private"] != true || fake.body["name"] != "notes" {
		t.Errorf("create body: %v", fake.body)
	}

	tree := strings.TrimSpace(run(t, remote, "ls-tree", "-r", "--name-only", "main"))
	if tree != "Daily/today.md\nattachments/shot.png" {
		t.Errorf("tree:\n%s", tree)
	}
	note := run(t, remote, "show", "main:Daily/today.md")
	if note != "![shot](../attachments/shot.png)\n[[missing]]\n" {
		t.Errorf("note: %q", note)
	}
	author := strings.TrimSpace(run(t, remote, "log", "-1", "--format=%an <%ae>", "main"))
	if author != "alice <7+alice@users.noreply.github.com>" {
		t.Errorf("author: %q", author)
	}

	sum := res.Summary()
	for _, want := range []string{"Создан приватный репозиторий alice/notes", "Заметок: 1, картинок: 1", "Daily/today.md: [[missing]]"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary missing %q:\n%s", want, sum)
		}
	}

	// Повторная отправка: репозиторий уже есть, изменений нет.
	res2, err := p.Publish(context.Background(), Request{Source: vaultDir, Token: "good", Repo: "alice/notes"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Created || res2.Changed {
		t.Errorf("second publish: %+v", res2)
	}
	if !strings.Contains(res2.Summary(), "Изменений нет") {
		t.Errorf("summary: %s", res2.Summary())
	}
}

func TestPublishBadToken(t *testing.T) {
	srv := httptest.NewServer(&fakeGitHub{t: t})
	defer srv.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0o644)

	_, err := (&Publisher{APIBaseURL: srv.URL}).Publish(context.Background(), Request{Source: dir, Token: "bad", Repo: "notes"})
	if err == nil || !strings.Contains(err.Error(), "отклонил токен") {
		t.Errorf("expected token error, got %v", err)
	}
}

func TestPublishRefusesEmptyVault(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pic.png"), []byte("p"), 0o644)
	_, err := (&Publisher{APIBaseURL: "http://127.0.0.1:1"}).Publish(context.Background(), Request{Source: dir, Token: "x", Repo: "notes"})
	if err == nil || !strings.Contains(err.Error(), "не найдено ни одной .md") {
		t.Errorf("expected empty vault error, got %v", err)
	}
}

func TestParseRepo(t *testing.T) {
	tests := []struct{ in, owner, name string }{
		{"notes", "", "notes"},
		{"alice/notes", "alice", "notes"},
		{"https://github.com/alice/notes.git", "alice", "notes"},
		{"github.com/alice/my.vault/", "alice", "my.vault"},
	}
	for _, tt := range tests {
		owner, name, err := ParseRepo(tt.in)
		if err != nil || owner != tt.owner || name != tt.name {
			t.Errorf("ParseRepo(%q) = %q, %q, %v", tt.in, owner, name, err)
		}
	}
	for _, bad := range []string{"", "a/b/c", "my notes", "../x"} {
		if _, _, err := ParseRepo(bad); err == nil {
			t.Errorf("ParseRepo(%q): expected error", bad)
		}
	}
}
