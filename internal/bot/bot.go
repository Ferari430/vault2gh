// Package bot — Telegram-приёмник: пользователь задаёт токен и репозиторий,
// присылает zip-архив хранилища, бот публикует его на GitHub.
package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Ferari430/vault2gh/internal/github"
	"github.com/Ferari430/vault2gh/internal/publish"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot API отдаёт ботам файлы не больше 20 МБ.
const maxDownload = 20 << 20

const helpText = `Публикую хранилище Obsidian в репозиторий GitHub так, чтобы картинки и ссылки между заметками работали.

1. /token <токен> — GitHub-токен с доступом к репозиториям (сообщение с токеном я сразу удаляю).
2. /repo <имя или owner/имя> — куда публиковать. Если репозитория нет, создам приватный.
3. Пришлите zip-архив папки хранилища.

Репозиторий становится копией хранилища: файлы, удалённые из хранилища, удаляются и из репозитория (кроме папки .github).

/status — текущие настройки
/logout — забыть токен

Telegram даёт ботам скачивать файлы до 20 МБ. Для больших хранилищ используйте консольную утилиту vault2gh.`

type session struct {
	token string
	login string
	repo  string
	busy  bool
}

type Bot struct {
	api *tgbotapi.BotAPI
	pub *publish.Publisher
	log *slog.Logger

	mu       sync.Mutex
	sessions map[int64]*session // токены хранятся только в памяти и пропадают при перезапуске
	wg       sync.WaitGroup
}

func New(telegramToken string, pub *publish.Publisher, log *slog.Logger) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	commands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "help", Description: "Как пользоваться"},
		tgbotapi.BotCommand{Command: "token", Description: "Задать GitHub-токен"},
		tgbotapi.BotCommand{Command: "repo", Description: "Задать репозиторий"},
		tgbotapi.BotCommand{Command: "status", Description: "Текущие настройки"},
		tgbotapi.BotCommand{Command: "logout", Description: "Забыть токен"},
	)
	if _, err := api.Request(commands); err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	return &Bot{api: api, pub: pub, log: log, sessions: map[int64]*session{}}, nil
}

// Run обрабатывает сообщения до отмены ctx и дожидается начатых публикаций.
func (b *Bot) Run(ctx context.Context) error {
	b.log.Info("бот запущен", "username", b.api.Self.UserName)
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)
	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			b.wg.Wait()
			return nil
		case upd, ok := <-updates:
			if !ok {
				b.wg.Wait()
				return nil
			}
			if upd.Message != nil {
				b.handle(ctx, upd.Message)
			}
		}
	}
}

func (b *Bot) handle(ctx context.Context, m *tgbotapi.Message) {
	chat := m.Chat.ID
	if !m.Chat.IsPrivate() {
		if m.IsCommand() {
			b.reply(chat, "Я работаю только в личных сообщениях: здесь нужен GitHub-токен.")
		}
		return
	}
	if m.Document != nil {
		b.handleArchive(ctx, m)
		return
	}
	switch m.Command() {
	case "start", "help":
		b.reply(chat, helpText)
	case "token":
		b.handleToken(ctx, m)
	case "repo":
		b.handleRepo(m)
	case "status":
		b.handleStatus(chat)
	case "logout":
		b.mu.Lock()
		delete(b.sessions, chat)
		b.mu.Unlock()
		b.reply(chat, "Токен забыт.")
	default:
		b.reply(chat, "Пришлите zip-архив хранилища или /help.")
	}
}

func (b *Bot) handleToken(ctx context.Context, m *tgbotapi.Message) {
	chat := m.Chat.ID
	// Сообщение с токеном удаляем сразу, даже если токен окажется неверным.
	if _, err := b.api.Request(tgbotapi.NewDeleteMessage(chat, m.MessageID)); err != nil {
		b.log.Warn("не удалось удалить сообщение с токеном", "chat", chat, "err", err)
	}
	token := strings.TrimSpace(m.CommandArguments())
	if token == "" {
		b.reply(chat, "Использование: /token <GitHub-токен>")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	gh := github.NewClient(token)
	if b.pub.APIBaseURL != "" {
		gh.BaseURL = b.pub.APIBaseURL
	}
	user, err := gh.CurrentUser(ctx)
	if err != nil {
		b.reply(chat, "Токен не подошёл: "+err.Error())
		return
	}

	b.mu.Lock()
	s := b.sessionLocked(chat)
	s.token, s.login = token, user.Login
	repo := s.repo
	b.mu.Unlock()

	msg := "Токен принят, аккаунт GitHub: " + user.Login + "."
	if repo == "" {
		msg += "\nТеперь укажите репозиторий: /repo <имя>"
	} else {
		msg += "\nМожно присылать архив."
	}
	b.reply(chat, msg)
}

func (b *Bot) handleRepo(m *tgbotapi.Message) {
	chat := m.Chat.ID
	arg := strings.TrimSpace(m.CommandArguments())
	if arg == "" {
		b.reply(chat, "Использование: /repo <имя> или /repo <owner>/<имя>")
		return
	}
	if _, _, err := publish.ParseRepo(arg); err != nil {
		b.reply(chat, err.Error())
		return
	}
	b.mu.Lock()
	b.sessionLocked(chat).repo = arg
	b.mu.Unlock()
	b.reply(chat, "Репозиторий: "+arg+". Пришлите zip-архив хранилища.")
}

func (b *Bot) handleStatus(chat int64) {
	b.mu.Lock()
	s := *b.sessionLocked(chat)
	b.mu.Unlock()
	account := "токен не задан (/token)"
	if s.token != "" {
		account = "GitHub: " + s.login
	}
	repo := "не задан (/repo)"
	if s.repo != "" {
		repo = s.repo
	}
	b.reply(chat, account+"\nРепозиторий: "+repo)
}

func (b *Bot) handleArchive(ctx context.Context, m *tgbotapi.Message) {
	chat, doc := m.Chat.ID, m.Document
	if !strings.EqualFold(filepath.Ext(doc.FileName), ".zip") {
		b.reply(chat, "Нужен zip-архив папки хранилища.")
		return
	}
	if doc.FileSize > maxDownload {
		b.reply(chat, "Архив больше 20 МБ — Telegram не даст его скачать. Используйте консольную утилиту: vault2gh push <папка> --repo <имя>")
		return
	}

	b.mu.Lock()
	s := b.sessionLocked(chat)
	token, repo, busy := s.token, s.repo, s.busy
	if token != "" && repo != "" && !busy {
		s.busy = true
	}
	b.mu.Unlock()
	switch {
	case token == "":
		b.reply(chat, "Сначала задайте GitHub-токен: /token <токен>")
		return
	case repo == "":
		b.reply(chat, "Сначала укажите репозиторий: /repo <имя>")
		return
	case busy:
		b.reply(chat, "Предыдущий архив ещё обрабатывается, подождите.")
		return
	}

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			b.mu.Lock()
			if s, ok := b.sessions[chat]; ok {
				s.busy = false
			}
			b.mu.Unlock()
		}()
		b.publish(ctx, chat, doc.FileID, token, repo)
	}()
}

func (b *Bot) publish(ctx context.Context, chat int64, fileID, token, repo string) {
	b.reply(chat, "Скачиваю архив…")
	path, cleanup, err := b.download(ctx, fileID)
	if err != nil {
		b.log.Error("скачивание", "chat", chat, "err", err)
		b.reply(chat, "Не удалось скачать архив из Telegram, попробуйте ещё раз.")
		return
	}
	defer cleanup()

	pub := *b.pub
	pub.Progress = func(msg string) { b.reply(chat, msg) }
	res, err := pub.Publish(ctx, publish.Request{Source: path, Token: token, Repo: repo})
	if err != nil {
		b.log.Error("публикация", "chat", chat, "repo", repo, "err", err)
		b.reply(chat, "Не получилось: "+err.Error())
		return
	}
	b.log.Info("опубликовано", "chat", chat, "repo", res.Repo.FullName, "changed", res.Changed)
	b.reply(chat, res.Summary())
}

// download сохраняет файл из Telegram во временную папку. Ссылка на файл
// содержит токен бота, поэтому в ошибки она не попадает.
func (b *Bot) download(ctx context.Context, fileID string) (string, func(), error) {
	link, err := b.api.GetFileDirectURL(fileID)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", nil, errors.New("некорректная ссылка на файл")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, errors.New("сетевая ошибка при скачивании")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("telegram вернул %s", resp.Status)
	}

	dir, err := os.MkdirTemp("", "vault2gh-tg-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	path := filepath.Join(dir, "vault.zip")
	out, err := os.Create(path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, maxDownload+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = errors.New("файл больше 20 МБ")
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func (b *Bot) sessionLocked(chat int64) *session {
	s, ok := b.sessions[chat]
	if !ok {
		s = &session{}
		b.sessions[chat] = s
	}
	return s
}

// reply отправляет обычный текст (без разметки, чтобы пути и ссылки не ломали её).
func (b *Bot) reply(chat int64, text string) {
	const limit = 4000 // у Telegram лимит 4096 символов
	if r := []rune(text); len(r) > limit {
		text = string(r[:limit]) + "…"
	}
	msg := tgbotapi.NewMessage(chat, text)
	msg.DisableWebPagePreview = true
	if _, err := b.api.Send(msg); err != nil {
		b.log.Warn("не удалось отправить сообщение", "chat", chat, "err", err)
	}
}
