# vault2gh

Публикует хранилище Obsidian в репозиторий GitHub так, чтобы на github.com
отображались картинки и работали ссылки между заметками.

Пример результата: https://github.com/Ferari430/sobes-notes

## Быстрый старт

Нужны аккаунт GitHub, Git и терминал. Go устанавливать не нужно.

### 1. Установите Git

Через Git программа отправляет файлы на GitHub.

- **Windows:** скачайте и установите https://git-scm.com/download/win
  (все настройки можно оставить по умолчанию). После установки откройте
  **новое** окно PowerShell.
- **macOS:** откройте Терминал, выполните `xcode-select --install` и
  согласитесь на установку.
- **Linux:** `sudo apt install git` (или через пакетный менеджер вашего дистрибутива).

### 2. Создайте токен GitHub

Откройте https://github.com/settings/personal-access-tokens/new и заполните:

- **Token name** — любое, например `vault2gh`;
- **Expiration** — срок действия, например 90 дней;
- **Repository access** — *All repositories*;
- **Permissions → Repository permissions:**
  - *Contents* — **Read and write**;
  - *Administration* — **Read and write** (чтобы программа сама создала репозиторий).

Нажмите **Generate token** и скопируйте токен (`github_pat_…`): GitHub
показывает его один раз. Никому его не пересылайте — программа спросит его
при запуске и отправит только на GitHub.

### 3. Скачайте vault2gh

**Windows** (PowerShell):

```powershell
cd $HOME
curl.exe -fLo vault2gh.exe https://github.com/Ferari430/vault2gh/releases/latest/download/vault2gh_windows_amd64.exe
.\vault2gh.exe version
```

**macOS и Linux** (Терминал) — команда сама выберет файл под вашу систему и процессор:

```sh
cd ~
curl -fLo vault2gh "https://github.com/Ferari430/vault2gh/releases/latest/download/vault2gh_$(uname -s | tr A-Z a-z)_$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)"
chmod +x vault2gh
./vault2gh version
```

Последняя команда должна напечатать версию, например `vault2gh v0.1.0`.
Все файлы и контрольные суммы — на странице
[Releases](https://github.com/Ferari430/vault2gh/releases).

### 4. Отправьте хранилище

**Windows:**

```powershell
.\vault2gh.exe push "$HOME\Documents\MyVault" --repo my-notes
```

**macOS и Linux:**

```sh
./vault2gh push ~/Documents/MyVault --repo my-notes
```

Вместо `Documents/MyVault` укажите папку, которую вы открываете в Obsidian
как хранилище. Проще всего напечатать команду до `push `, перетащить папку
хранилища в окно терминала — путь вставится сам — и дописать `--repo my-notes`.

Программа спросит токен (ввод скрыт: вставьте токен и нажмите Enter),
создаст **приватный** репозиторий `my-notes`, отправит заметки и напечатает
ссылку на него. Чтобы сразу сделать репозиторий публичным, добавьте `--public`.

Обновить репозиторий после правок в Obsidian — та же команда ещё раз:
отправятся только изменения.

> В `--repo` указывайте новое имя или репозиторий только для заметок:
> репозиторий становится копией хранилища, и файлы, которых нет в хранилище,
> из него удаляются (история коммитов сохраняется).

### Если что-то пошло не так

| Сообщение | Что делать |
|---|---|
| `не найден git` | Установите Git (шаг 1) и откройте новое окно терминала |
| `xcrun: error: invalid active developer path` (macOS) | Выполните `xcode-select --install` |
| `GitHub отклонил токен` | Токен скопирован не полностью или истёк — создайте новый |
| `не удалось создать репозиторий … 403` | У токена нет права *Administration*. Добавьте его или создайте пустой репозиторий на https://github.com/new и запустите команду снова |
| `в хранилище не найдено ни одной .md заметки` | Неверный путь — укажите папку хранилища Obsidian |
| `токен не задан, а терминала для ввода нет` | Запускайте в обычном терминале или задайте токен в той же команде: `GITHUB_TOKEN=... ./vault2gh push …` |

## Что программа делает с хранилищем

- заметки (`.md`) остаются на своих местах;
- все картинки собираются в папку `attachments/` в корне репозитория;
- ссылки переписываются в обычный markdown с путями относительно каждой заметки:

| В Obsidian | В репозитории |
|---|---|
| `![[Pasted image.png]]` | `![Pasted image](../attachments/Pasted%20image.png)` |
| `![[схема.png\|300]]` | `<img src="attachments/схема.png" alt="схема" width="300">` |
| `![](img/pic.png)` | `![](attachments/pic.png)` |
| `[[Другая заметка#Раздел\|см.]]` | `[см.](Папка/Другая%20заметка.md#раздел)` |
| `![[Заметка]]`, `![[file.pdf]]` | ссылка (GitHub не умеет встраивать) |

Код (```` ``` ````-блоки и `` `инлайн` ``) и front matter не меняются. Ссылки,
для которых в хранилище нет файла, остаются как есть и выводятся в отчёте.
Картинки с одинаковыми именами из разных папок не перезаписывают друг друга:
одинаковые по содержимому склеиваются, разные получают суффикс из хэша.
Папки `.obsidian`, `.trash` и другие скрытые файлы не публикуются, файлы
больше 100 МБ пропускаются (GitHub их не принимает).

GitHub не отображает возможности плагинов Obsidian (Dataview, Excalidraw,
canvas) — такие файлы попадают в репозиторий как есть.

## Команды

```sh
vault2gh push <папка|архив.zip> --repo <имя|owner/имя> [--public] [--branch ветка] [-m "сообщение"] [--attachments папка]
vault2gh export <папка|архив.zip> <папка-результат>   # то же преобразование локально, без GitHub
vault2gh bot                                          # Telegram-бот
vault2gh version
```

Токен для `push` берётся из переменной `GITHUB_TOKEN`, флага `--token`,
из stdin с флагом `--token-stdin` (`echo "$T" | vault2gh push … --token-stdin`)
или вводится в терминале со скрытым вводом. Токен передаётся git через
переменные окружения и не сохраняется ни на диске, ни в `.git/config`.
Подойдёт и classic-токен с правом `repo`.

## Telegram-бот

```sh
TELEGRAM_BOT_TOKEN=123:abc vault2gh bot
```

В личном чате с ботом:

1. `/token <GitHub-токен>` — бот проверяет токен и сразу удаляет сообщение;
2. `/repo my-notes`;
3. отправить zip-архив папки хранилища.

Ограничения бота: архив до 20 МБ (лимит Bot API), токены хранятся только в
памяти и пропадают при перезапуске. Для больших хранилищ — консольная команда.

## Сборка из исходников

Нужны Go 1.25+ и git 2.31+.

```sh
go install github.com/Ferari430/vault2gh/cmd/vault2gh@latest
# или
git clone https://github.com/Ferari430/vault2gh && cd vault2gh && go build -o vault2gh ./cmd/vault2gh
```

## Устройство

```
cmd/vault2gh       CLI: push, export, bot, version
internal/source    папка или .zip → папка хранилища (защита от zip-slip и zip-бомб)
internal/vault     сбор картинок, переписывание ссылок
internal/github    REST API: проверка токена, поиск и создание репозитория
internal/gitpush   зеркалирование папки в ветку через системный git
internal/publish   общий сценарий для CLI и бота
internal/bot       Telegram-приёмник
```

Файлы отправляются через git, а не через REST API: у API есть лимит на
создание контента (около 80 запросов в минуту), в который упирается
хранилище с сотнями картинок.
