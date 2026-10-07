// Package gitpush делает ветку удалённого репозитория зеркалом папки:
// забирает ветку (только последний коммит), заменяет её содержимое,
// коммитит и отправляет. Используется системный git.
package gitpush

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type Options struct {
	RemoteURL string
	// Token — токен GitHub. Передаётся git через переменные окружения, а не
	// через URL, поэтому не попадает ни в .git/config, ни в список процессов.
	Token       string
	Branch      string
	Message     string
	AuthorName  string
	AuthorEmail string
	// Keep — записи в корне репозитория, которые не удаляются (например, .github).
	Keep []string
}

type Result struct {
	Changed bool   // false — содержимое уже совпадало, коммит не создавался
	Commit  string // SHA вершины ветки после отправки
}

// Push заменяет содержимое ветки тем, что fill запишет в переданную папку.
func Push(ctx context.Context, o Options, fill func(dir string) error) (*Result, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("не найден git: установите его и повторите")
	}
	if o.Branch == "" || strings.HasPrefix(o.Branch, "-") {
		return nil, fmt.Errorf("недопустимое имя ветки %q", o.Branch)
	}

	dir, err := os.MkdirTemp("", "vault2gh-git-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	g := &git{ctx: ctx, dir: dir, token: o.Token}

	if _, err := g.run("check-ref-format", "--branch", o.Branch); err != nil {
		return nil, fmt.Errorf("недопустимое имя ветки %q", o.Branch)
	}
	ref := "refs/heads/" + o.Branch
	if _, err := g.run("init", "-q"); err != nil {
		return nil, err
	}
	if _, err := g.run("remote", "add", "origin", o.RemoteURL); err != nil {
		return nil, err
	}
	heads, err := g.run("ls-remote", "origin", ref)
	if err != nil {
		return nil, err
	}
	exists := strings.TrimSpace(heads) != ""
	if exists {
		if _, err := g.run("fetch", "-q", "--depth=1", "origin", ref); err != nil {
			return nil, err
		}
		if _, err := g.run("checkout", "-q", "-B", o.Branch, "FETCH_HEAD"); err != nil {
			return nil, err
		}
	} else if _, err := g.run("symbolic-ref", "HEAD", ref); err != nil {
		return nil, err
	}

	if err := clearWorktree(dir, o.Keep); err != nil {
		return nil, err
	}
	if err := fill(dir); err != nil {
		return nil, err
	}
	if _, err := g.run("add", "-A"); err != nil {
		return nil, err
	}

	// diff --quiet: код 0 — изменений нет, 1 — есть, остальное — ошибка.
	_, err = g.run("diff", "--cached", "--quiet")
	var exitErr *exec.ExitError
	switch {
	case err == nil && exists:
		head, err := g.run("rev-parse", "HEAD")
		return &Result{Changed: false, Commit: strings.TrimSpace(head)}, err
	case err == nil:
		return nil, errors.New("нечего отправлять: папка пуста")
	case !errors.As(err, &exitErr) || exitErr.ExitCode() != 1:
		return nil, err
	}

	if _, err := g.run(
		"-c", "user.name="+o.AuthorName, "-c", "user.email="+o.AuthorEmail, "-c", "commit.gpgsign=false",
		"commit", "-q", "--no-verify", "-m", o.Message,
	); err != nil {
		return nil, err
	}
	if _, err := g.run("push", "-q", "--no-verify", "origin", "HEAD:"+ref); err != nil {
		return nil, err
	}
	head, err := g.run("rev-parse", "HEAD")
	return &Result{Changed: true, Commit: strings.TrimSpace(head)}, err
}

func clearWorktree(dir string, keep []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" || slices.Contains(keep, e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

type git struct {
	ctx   context.Context
	dir   string
	token string
}

func (g *git) run(args ...string) (string, error) {
	cmd := exec.CommandContext(g.ctx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = g.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := g.redact(strings.TrimSpace(stderr.String()))
		if msg == "" {
			return stdout.String(), err
		}
		return stdout.String(), &cmdError{args: args[0], msg: msg, err: err}
	}
	return stdout.String(), nil
}

func (g *git) env() []string {
	cfg := [][2]string{
		{"credential.helper", ""}, // не брать и не сохранять чужие учётные данные
		{"init.defaultBranch", "main"},
		{"advice.detachedHead", "false"},
	}
	if g.token != "" {
		cfg = append(cfg, [2]string{"http.https://github.com/.extraheader", "AUTHORIZATION: basic " + g.basicAuth()})
	}
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https:file",
		"GIT_CONFIG_COUNT="+strconv.Itoa(len(cfg)),
	)
	for i, kv := range cfg {
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	return env
}

func (g *git) basicAuth() string {
	return base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.token))
}

func (g *git) redact(s string) string {
	if g.token == "" {
		return s
	}
	return strings.NewReplacer(g.token, "***", g.basicAuth(), "***").Replace(s)
}

// cmdError сохраняет код выхода git (через Unwrap) и показывает его вывод.
type cmdError struct {
	args string
	msg  string
	err  error
}

func (e *cmdError) Error() string { return "git " + e.args + ": " + e.msg }
func (e *cmdError) Unwrap() error { return e.err }
