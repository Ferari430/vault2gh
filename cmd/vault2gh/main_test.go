package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// pipe возвращает читающий конец канала: это stdin без терминала, как при
// запуске из IDE или через «!» в Claude Code.
func pipe(t *testing.T, input string, closeWriter bool) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	if input != "" {
		w.WriteString(input)
	}
	if closeWriter {
		w.Close()
	}
	return r
}

func readTokenWithin(t *testing.T, ctx context.Context, stdin *os.File, flagValue string, fromStdin bool) (string, error) {
	t.Helper()
	type result struct {
		tok string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		tok, err := readToken(ctx, stdin, flagValue, fromStdin)
		ch <- result{tok, err}
	}()
	select {
	case r := <-ch:
		return r.tok, r.err
	case <-time.After(2 * time.Second):
		t.Fatal("readToken завис")
		return "", nil
	}
}

func TestReadTokenWithoutTerminalFailsFast(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	// Канал открыт, но в него никто не пишет: раньше здесь было молчаливое зависание.
	_, err := readTokenWithin(t, context.Background(), pipe(t, "", false), "", false)
	if err == nil || !strings.Contains(err.Error(), "терминала для ввода нет") {
		t.Fatalf("expected no-terminal error, got %v", err)
	}
}

func TestReadTokenSources(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GITHUB_TOKEN", "from-env")

	if tok, _ := readTokenWithin(t, ctx, pipe(t, "", false), "from-flag", false); tok != "from-flag" {
		t.Errorf("flag should win, got %q", tok)
	}
	if tok, _ := readTokenWithin(t, ctx, pipe(t, "", false), "", false); tok != "from-env" {
		t.Errorf("env expected, got %q", tok)
	}
	for _, in := range []string{"from-stdin\n", "from-stdin", "  from-stdin \r\n"} {
		tok, err := readTokenWithin(t, ctx, pipe(t, in, true), "", true)
		if err != nil || tok != "from-stdin" {
			t.Errorf("stdin %q: got %q, %v", in, tok, err)
		}
	}
	if _, err := readTokenWithin(t, ctx, pipe(t, "\n", true), "", true); err == nil {
		t.Error("empty stdin token should fail")
	}
}

func TestReadTokenStdinCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := readTokenWithin(t, ctx, pipe(t, "", false), "", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
