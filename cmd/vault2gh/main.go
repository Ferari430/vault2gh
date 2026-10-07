// Команда vault2gh публикует хранилище Obsidian в репозиторий GitHub так,
// чтобы картинки и ссылки между заметками отображались на github.com.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/Ferari430/vault2gh/internal/bot"
	"github.com/Ferari430/vault2gh/internal/publish"
	"github.com/Ferari430/vault2gh/internal/source"
	"github.com/Ferari430/vault2gh/internal/vault"
	"golang.org/x/term"
)

const usage = `vault2gh — публикация хранилища Obsidian на GitHub.

Использование:
  vault2gh push <папка|архив.zip> --repo <имя|owner/имя> [флаги]
      Обработать хранилище и отправить в репозиторий. Если репозитория нет,
      он создаётся (приватным). Репозиторий становится копией хранилища:
      удалённые из хранилища файлы удаляются и из репозитория (кроме .github/).
      Токен: переменная GITHUB_TOKEN, --token, --token-stdin или скрытый ввод
      в терминале.

  vault2gh export <папка|архив.zip> <папка-результат>
      Сделать то же преобразование локально, без GitHub — посмотреть результат
      или отправить самостоятельно через git.

  vault2gh bot
      Запустить Telegram-бота. Токен бота — в переменной TELEGRAM_BOT_TOKEN.

  vault2gh version
      Показать версию.

Запустите "vault2gh <команда> -h", чтобы увидеть флаги команды.`

// version подставляется при сборке релиза: -ldflags "-X main.version=v0.1.0".
var version = "dev"

// versionString для сборок через go install берёт версию модуля из сборочной информации.
func versionString() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Первый Ctrl+C отменяет работу штатно, после него возвращаем обычное
	// поведение сигналов: второй Ctrl+C завершает программу сразу.
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := run(ctx, os.Args[1:]); err != nil {
		switch {
		case errors.Is(err, flag.ErrHelp):
		case errors.Is(err, context.Canceled):
			fmt.Fprintln(os.Stderr, "прервано")
		default:
			fmt.Fprintln(os.Stderr, "ошибка:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "push":
		return cmdPush(ctx, args[1:])
	case "export":
		return cmdExport(args[1:])
	case "bot":
		return cmdBot(ctx, args[1:])
	case "version", "--version":
		fmt.Println("vault2gh", versionString())
		return nil
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fmt.Errorf("неизвестная команда %q\n\n%s", args[0], usage)
	}
}

func cmdPush(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	repo := fs.String("repo", "", "репозиторий: имя, owner/имя или ссылка (обязательно)")
	branch := fs.String("branch", "", "ветка (по умолчанию — основная ветка репозитория)")
	public := fs.Bool("public", false, "создать публичный репозиторий, если его нет")
	message := fs.String("m", "", "сообщение коммита")
	attachments := fs.String("attachments", vault.DefaultAttachmentsDir, "папка для картинок в репозитории")
	token := fs.String("token", "", "GitHub-токен (надёжнее через GITHUB_TOKEN: флаг остаётся в истории shell)")
	tokenStdin := fs.Bool("token-stdin", false, `прочитать токен из stdin: echo "$T" | vault2gh push … --token-stdin`)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *repo == "" {
		return errors.New("использование: vault2gh push <папка|архив.zip> --repo <имя>")
	}

	tok, err := readToken(ctx, os.Stdin, *token, *tokenStdin)
	if err != nil {
		return err
	}
	p := &publish.Publisher{Progress: func(s string) { fmt.Fprintln(os.Stderr, s) }}
	res, err := p.Publish(ctx, publish.Request{
		Source:      pos[0],
		Token:       tok,
		Repo:        *repo,
		Branch:      *branch,
		Public:      *public,
		Message:     *message,
		Attachments: *attachments,
	})
	if err != nil {
		return err
	}
	fmt.Println(res.Summary())
	return nil
}

func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	attachments := fs.String("attachments", vault.DefaultAttachmentsDir, "папка для картинок")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("использование: vault2gh export <папка|архив.zip> <папка-результат>")
	}
	out := pos[1]
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s не пуста: укажите новую или пустую папку", out)
	}

	root, cleanup, err := source.Open(pos[0])
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := vault.Build(root, vault.Options{AttachmentsDir: *attachments})
	if err != nil {
		return err
	}
	if err := res.Export(out); err != nil {
		return err
	}
	fmt.Printf("Заметок: %d, картинок: %d, других файлов: %d → %s\n", res.Stats.Notes, res.Stats.Images, res.Stats.Other, out)
	for _, u := range res.Unresolved {
		fmt.Printf("  нет файла: %s: %s\n", u.Note, u.Link)
	}
	for _, s := range res.Skipped {
		fmt.Printf("  пропущен (больше 100 МБ): %s\n", s)
	}
	return nil
}

func cmdBot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bot", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return errors.New("задайте токен бота в переменной TELEGRAM_BOT_TOKEN")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	b, err := bot.New(token, &publish.Publisher{}, log)
	if err != nil {
		return err
	}
	return b.Run(ctx)
}

// parse разрешает флаги и после позиционных аргументов:
// "push ./vault --repo x" и "push --repo x ./vault" работают одинаково.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// readToken берёт токен из флага --token, из stdin (--token-stdin), из
// GITHUB_TOKEN или спрашивает в терминале со скрытым вводом. Без терминала
// молча ждать ввод нельзя — со стороны это выглядит как зависание с пустым
// выводом, поэтому сразу возвращается ошибка.
func readToken(ctx context.Context, stdin *os.File, flagValue string, fromStdin bool) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" && !fromStdin {
		return t, nil
	}

	fd := int(stdin.Fd())
	var t string
	var err error
	switch {
	case term.IsTerminal(fd):
		t, err = promptHidden(ctx, fd)
	case fromStdin:
		r := bufio.NewReader(stdin)
		t, err = readCtx(ctx, func() (string, error) { return r.ReadString('\n') })
		if errors.Is(err, io.EOF) {
			err = nil
		}
	default:
		return "", errors.New("токен не задан, а терминала для ввода нет: задайте GITHUB_TOKEN в той же команде " +
			"(GITHUB_TOKEN=... vault2gh push …), передайте его через --token-stdin или запустите в обычном терминале")
	}
	if err != nil {
		return "", err
	}
	if t = strings.TrimSpace(t); t == "" {
		return "", errors.New("введён пустой токен")
	}
	return t, nil
}

func promptHidden(ctx context.Context, fd int) (string, error) {
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "GitHub-токен (ввод скрыт): ")
	t, err := readCtx(ctx, func() (string, error) {
		b, err := term.ReadPassword(fd)
		return string(b), err
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		term.Restore(fd, state) // если ввод прервали, ReadPassword не успел вернуть эхо
	}
	return t, err
}

// readCtx выполняет блокирующее чтение в горутине, чтобы Ctrl+C его прерывал.
func readCtx(ctx context.Context, read func() (string, error)) (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := read()
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
