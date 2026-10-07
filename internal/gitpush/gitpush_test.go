package gitpush

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFiles(files map[string]string) func(string) error {
	return func(dir string) error {
		for name, content := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

func listTree(t *testing.T, remote, branch string) string {
	t.Helper()
	return strings.TrimSpace(gitCmd(t, remote, "ls-tree", "-r", "--name-only", branch))
}

func TestPushMirrorsFolder(t *testing.T) {
	remote := t.TempDir()
	gitCmd(t, remote, "init", "-q", "--bare", "-b", "main")
	opts := Options{RemoteURL: remote, Branch: "main", Message: "sync", AuthorName: "me", AuthorEmail: "me@x", Keep: []string{".github"}}
	ctx := context.Background()

	// 1. Пустой репозиторий: создаётся первая версия ветки.
	res, err := Push(ctx, opts, writeFiles(map[string]string{"a.md": "a", "attachments/p.png": "p", "b.md": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || len(res.Commit) != 40 {
		t.Fatalf("first push: %+v", res)
	}
	if got := listTree(t, remote, "main"); got != "a.md\nattachments/p.png\nb.md" {
		t.Fatalf("tree after first push:\n%s", got)
	}
	author := strings.TrimSpace(gitCmd(t, remote, "log", "-1", "--format=%an <%ae> %s", "main"))
	if author != "me <me@x> sync" {
		t.Errorf("author/message = %q", author)
	}

	// 2. То же содержимое — коммита нет.
	res2, err := Push(ctx, opts, writeFiles(map[string]string{"a.md": "a", "attachments/p.png": "p", "b.md": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed || res2.Commit != res.Commit {
		t.Errorf("second push should be no-op: %+v (first %s)", res2, res.Commit)
	}

	// 3. Кто-то добавил .github в репозиторий; из хранилища удалили b.md.
	work := t.TempDir()
	gitCmd(t, work, "clone", "-q", remote, ".")
	os.MkdirAll(filepath.Join(work, ".github"), 0o755)
	os.WriteFile(filepath.Join(work, ".github", "w.yml"), []byte("w"), 0o644)
	gitCmd(t, work, "add", "-A")
	gitCmd(t, work, "commit", "-q", "-m", "ci")
	gitCmd(t, work, "push", "-q", "origin", "main")

	res3, err := Push(ctx, opts, writeFiles(map[string]string{"a.md": "a2", "attachments/p.png": "p"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res3.Changed {
		t.Fatal("third push should change")
	}
	if got := listTree(t, remote, "main"); got != ".github/w.yml\na.md\nattachments/p.png" {
		t.Errorf("tree after mirror:\n%s", got)
	}
	if n := strings.TrimSpace(gitCmd(t, remote, "rev-list", "--count", "main")); n != "3" {
		t.Errorf("history should be kept, commits = %s", n)
	}
}

func TestPushNewBranchInExistingRepo(t *testing.T) {
	remote := t.TempDir()
	gitCmd(t, remote, "init", "-q", "--bare", "-b", "main")
	ctx := context.Background()
	base := Options{RemoteURL: remote, Message: "m", AuthorName: "me", AuthorEmail: "me@x"}

	o := base
	o.Branch = "main"
	if _, err := Push(ctx, o, writeFiles(map[string]string{"a.md": "a"})); err != nil {
		t.Fatal(err)
	}
	o.Branch = "vault"
	if _, err := Push(ctx, o, writeFiles(map[string]string{"v.md": "v"})); err != nil {
		t.Fatal(err)
	}
	if got := listTree(t, remote, "vault"); got != "v.md" {
		t.Errorf("vault branch = %q", got)
	}
	if got := listTree(t, remote, "main"); got != "a.md" {
		t.Errorf("main branch changed: %q", got)
	}
}

func TestPushRejectsBadBranch(t *testing.T) {
	for _, b := range []string{"", "--upload-pack=x", "a..b", "bad branch"} {
		_, err := Push(context.Background(), Options{RemoteURL: t.TempDir(), Branch: b}, writeFiles(nil))
		if err == nil || !strings.Contains(err.Error(), "ветки") {
			t.Errorf("branch %q: expected error, got %v", b, err)
		}
	}
}

func TestPushEmptyFolder(t *testing.T) {
	remote := t.TempDir()
	gitCmd(t, remote, "init", "-q", "--bare", "-b", "main")
	_, err := Push(context.Background(), Options{RemoteURL: remote, Branch: "main"}, writeFiles(nil))
	if err == nil || !strings.Contains(err.Error(), "пуста") {
		t.Errorf("expected empty error, got %v", err)
	}
}

func TestRedact(t *testing.T) {
	g := &git{token: "ghp_secret"}
	s := g.redact("fatal: ghp_secret and " + g.basicAuth())
	if strings.Contains(s, "ghp_secret") || strings.Contains(s, g.basicAuth()) {
		t.Errorf("token leaked: %q", s)
	}
}
