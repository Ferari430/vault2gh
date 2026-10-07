// Package github — минимальный клиент GitHub REST API: проверка токена,
// поиск и создание репозитория. Сами файлы отправляются через git (пакет gitpush).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.github.com"

var ErrNotFound = errors.New("не найдено")

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub API: %d %s", e.Status, e.Message)
}

type Client struct {
	BaseURL string
	token   string
	http    *http.Client
}

func NewClient(token string) *Client {
	return &Client{BaseURL: DefaultBaseURL, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// NoreplyEmail — адрес, по которому GitHub привязывает коммиты к аккаунту,
// не раскрывая настоящую почту.
func (u *User) NoreplyEmail() string {
	return fmt.Sprintf("%d+%s@users.noreply.github.com", u.ID, u.Login)
}

type Repo struct {
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			return nil, errors.New("GitHub отклонил токен: он неверный или просрочен")
		}
		return nil, err
	}
	return &u, nil
}

// GetRepo возвращает ErrNotFound, если репозитория нет или у токена нет к нему доступа.
func (c *Client) GetRepo(ctx context.Context, owner, name string) (*Repo, error) {
	var r Repo
	if err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateRepo создаёт пустой репозиторий у пользователя login или в организации owner.
func (c *Client) CreateRepo(ctx context.Context, owner, name string, private bool, login string) (*Repo, error) {
	endpoint := "/user/repos"
	if !strings.EqualFold(owner, login) {
		endpoint = "/orgs/" + url.PathEscape(owner) + "/repos"
	}
	body := map[string]any{
		"name":        name,
		"private":     private,
		"description": "Obsidian vault",
		"auto_init":   false,
	}
	var r Repo
	if err := c.do(ctx, http.MethodPost, endpoint, body, &r); err != nil {
		return nil, fmt.Errorf("не удалось создать репозиторий %s/%s: %w", owner, name, err)
	}
	return &r, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "vault2gh")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		msg := e.Message
		for _, sub := range e.Errors {
			if sub.Message != "" {
				msg += ": " + sub.Message
			}
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
