// Package publish — общий сценарий для всех приёмников (CLI, Telegram):
// хранилище → обработка → репозиторий на GitHub.
package publish

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Ferari430/vault2gh/internal/github"
	"github.com/Ferari430/vault2gh/internal/gitpush"
	"github.com/Ferari430/vault2gh/internal/source"
	"github.com/Ferari430/vault2gh/internal/vault"
)

type Request struct {
	Source      string // папка хранилища или .zip
	Token       string // токен GitHub
	Repo        string // "name", "owner/name" или ссылка на репозиторий
	Branch      string // пусто — ветка по умолчанию
	Public      bool   // видимость, если репозиторий придётся создать
	Message     string // сообщение коммита
	Attachments string // папка для картинок, пусто — vault.DefaultAttachmentsDir
}

type Result struct {
	Repo    *github.Repo
	Branch  string
	Created bool
	Changed bool
	Commit  string
	Vault   *vault.Result
}

type Publisher struct {
	APIBaseURL string       // пусто — api.github.com
	Progress   func(string) // необязательно: сообщения о ходе работы
}

func (p *Publisher) Publish(ctx context.Context, req Request) (*Result, error) {
	owner, name, err := ParseRepo(req.Repo)
	if err != nil {
		return nil, err
	}

	root, cleanup, err := source.Open(req.Source)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	p.progress("Обрабатываю хранилище…")
	vres, err := vault.Build(root, vault.Options{AttachmentsDir: req.Attachments})
	if err != nil {
		return nil, err
	}
	// Репозиторий становится зеркалом хранилища, поэтому пустой результат
	// (например, указана не та папка) стёр бы всё содержимое.
	if vres.Stats.Notes == 0 {
		return nil, errors.New("в хранилище не найдено ни одной .md заметки — проверьте путь; репозиторий не тронут")
	}

	gh := github.NewClient(req.Token)
	if p.APIBaseURL != "" {
		gh.BaseURL = p.APIBaseURL
	}
	user, err := gh.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if owner == "" {
		owner = user.Login
	}

	res := &Result{Vault: vres}
	res.Repo, err = gh.GetRepo(ctx, owner, name)
	if errors.Is(err, github.ErrNotFound) {
		p.progress(fmt.Sprintf("Создаю репозиторий %s/%s…", owner, name))
		res.Repo, err = gh.CreateRepo(ctx, owner, name, !req.Public, user.Login)
		res.Created = true
	}
	if err != nil {
		return nil, err
	}

	res.Branch = req.Branch
	if res.Branch == "" {
		res.Branch = res.Repo.DefaultBranch
	}
	if res.Branch == "" {
		res.Branch = "main"
	}
	msg := req.Message
	if msg == "" {
		msg = "Синхронизация хранилища " + time.Now().Format("2006-01-02 15:04")
	}

	p.progress(fmt.Sprintf("Отправляю в %s (ветка %s)…", res.Repo.FullName, res.Branch))
	pushed, err := gitpush.Push(ctx, gitpush.Options{
		RemoteURL:   res.Repo.CloneURL,
		Token:       req.Token,
		Branch:      res.Branch,
		Message:     msg,
		AuthorName:  user.Login,
		AuthorEmail: user.NoreplyEmail(),
		Keep:        []string{".github"},
	}, vres.Export)
	if err != nil {
		return nil, err
	}
	res.Changed, res.Commit = pushed.Changed, pushed.Commit
	return res, nil
}

func (p *Publisher) progress(msg string) {
	if p.Progress != nil {
		p.Progress(msg)
	}
}

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ParseRepo разбирает "name", "owner/name" и ссылки вида
// https://github.com/owner/name(.git). Пустой owner означает владельца токена.
func ParseRepo(s string) (owner, name string, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 1:
		name = parts[0]
	case 2:
		owner, name = parts[0], parts[1]
	default:
		return "", "", fmt.Errorf("не понял репозиторий %q: ожидается имя или owner/имя", s)
	}
	if !validName(name) || owner != "" && !validName(owner) {
		return "", "", fmt.Errorf("недопустимое имя репозитория %q", s)
	}
	return owner, name, nil
}

func validName(s string) bool {
	return repoNameRe.MatchString(s) && s != "." && s != ".."
}

// Summary — отчёт для пользователя, общий для CLI и бота.
func (r *Result) Summary() string {
	var b strings.Builder
	s := r.Vault.Stats
	if r.Created {
		visibility := "приватный"
		if !r.Repo.Private {
			visibility = "публичный"
		}
		fmt.Fprintf(&b, "Создан %s репозиторий %s\n", visibility, r.Repo.FullName)
	}
	fmt.Fprintf(&b, "Заметок: %d, картинок: %d, других файлов: %d\n", s.Notes, s.Images, s.Other)
	if r.Changed {
		fmt.Fprintf(&b, "Готово: %s (ветка %s, коммит %.7s)\n", r.Repo.HTMLURL, r.Branch, r.Commit)
	} else {
		fmt.Fprintf(&b, "Изменений нет: %s уже совпадает с хранилищем\n", r.Repo.HTMLURL)
	}
	if n := len(r.Vault.Skipped); n > 0 {
		fmt.Fprintf(&b, "\nПропущено файлов больше 100 МБ (GitHub их не принимает): %d\n", n)
		for _, f := range r.Vault.Skipped {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	if n := len(r.Vault.Unresolved); n > 0 {
		const limit = 15
		fmt.Fprintf(&b, "\nСсылки без файла в хранилище (оставлены как есть): %d\n", n)
		for i, u := range r.Vault.Unresolved {
			if i == limit {
				fmt.Fprintf(&b, "  …и ещё %d\n", n-limit)
				break
			}
			fmt.Fprintf(&b, "  %s: %s\n", u.Note, u.Link)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
