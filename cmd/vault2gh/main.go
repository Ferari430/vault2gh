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
      Токен: --token, переменная GITHUB_TOKEN или ввод с клавиатуры.

  vault2gh export <папка|архив.zip> <папка-результат>
      Сделать то же преобразование локально, без GitHub — посмотреть результат
      или отправить самостоятельно через git.

  vault2gh bot
      Запустить Telegram-бота. Токен бота — в переменной TELEGRAM_BOT_TOKEN.

Запустите "vault2gh <команда> -h", чтобы увидеть флаги команды.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
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
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *repo == "" {
		return errors.New("использование: vault2gh push <папка|архив.zip> --repo <имя>")
	}

	tok, err := readToken(*token)
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

// readToken берёт токен из флага, GITHUB_TOKEN, скрытого ввода в терминале
// или первой строки stdin (echo "$T" | vault2gh push …).
func readToken(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t, nil
	}
	fd := int(os.Stdin.Fd())
	var t string
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "GitHub-токен (ввод скрыт): ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		t = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		t = line
	}
	if t = strings.TrimSpace(t); t == "" {
		return "", errors.New("токен не задан: используйте GITHUB_TOKEN, --token или введите его при запуске")
	}
	return t, nil
}
