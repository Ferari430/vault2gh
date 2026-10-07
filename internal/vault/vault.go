// Package vault готовит хранилище Obsidian к публикации на GitHub: картинки
// собираются в одну папку, а ссылки на них и на заметки переписываются в
// стандартный markdown с путями относительно каждой заметки.
package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DefaultAttachmentsDir — папка в репозитории, куда собираются картинки.
const DefaultAttachmentsDir = "attachments"

// MaxFileSize — GitHub не принимает файлы больше 100 МБ, такие файлы пропускаются.
const MaxFileSize = 100 << 20

type Kind int

const (
	KindNote Kind = iota
	KindImage
	KindOther
)

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".svg": true, ".webp": true, ".bmp": true, ".avif": true,
}

type Options struct {
	// AttachmentsDir — папка для картинок относительно корня репозитория.
	AttachmentsDir string
}

// File — файл, который попадёт в репозиторий.
type File struct {
	Path string // путь в репозитории через "/"
	Kind Kind

	source string // файл в хранилище
	data   []byte // переписанная заметка
}

type Unresolved struct {
	Note string // заметка, в которой встретилась ссылка
	Link string // ссылка в исходном виде
}

type Stats struct {
	Notes, Images, Other int
}

type Result struct {
	Files      []File
	Stats      Stats
	Unresolved []Unresolved // ссылки, для которых не нашлось файла; оставлены как есть
	Skipped    []string     // файлы больше MaxFileSize
}

type entry struct {
	rel  string // путь в хранилище через "/"
	abs  string
	kind Kind
	out  string // путь в репозитории
	dup  bool   // копия уже добавленной картинки: ссылки ведут на неё, отдельно не пишется
}

// Build читает хранилище из root и возвращает набор файлов для репозитория.
// Хранилище не изменяется.
func Build(root string, opts Options) (*Result, error) {
	att, err := attachmentsDir(opts.AttachmentsDir)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}

	res := &Result{}
	entries, err := scan(root, res)
	if err != nil {
		return nil, err
	}
	if err := placeImages(entries, att); err != nil {
		return nil, err
	}

	idx := newIndex(entries)
	for _, e := range entries {
		if e.dup {
			continue
		}
		f := File{Path: e.out, Kind: e.kind, source: e.abs}
		switch e.kind {
		case KindNote:
			src, err := os.ReadFile(e.abs)
			if err != nil {
				return nil, err
			}
			rw := rewriter{idx: idx, note: e, res: res, seen: map[string]bool{}}
			f.data = rw.rewrite(src)
			res.Stats.Notes++
		case KindImage:
			res.Stats.Images++
		default:
			res.Stats.Other++
		}
		res.Files = append(res.Files, f)
	}
	return res, nil
}

// Export записывает файлы результата в папку dir.
func (r *Result) Export(dir string) error {
	for _, f := range r.Files {
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		var err error
		if f.Kind == KindNote {
			err = os.WriteFile(dst, f.data, 0o644)
		} else {
			err = copyFile(f.source, dst)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func attachmentsDir(dir string) (string, error) {
	if dir == "" {
		return DefaultAttachmentsDir, nil
	}
	clean := path.Clean(filepath.ToSlash(dir))
	if clean == "." || strings.HasPrefix(clean, ".") || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("папка для картинок должна быть относительным путём внутри репозитория: %q", dir)
	}
	return clean, nil
}

// scan собирает файлы хранилища. Скрытые файлы и папки (.obsidian, .trash, .git)
// и симлинки пропускаются. WalkDir обходит в лексическом порядке, поэтому
// результат детерминирован.
func scan(root string, res *Result) ([]*entry, error) {
	var entries []*entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxFileSize {
			res.Skipped = append(res.Skipped, rel)
			return nil
		}
		entries = append(entries, &entry{rel: rel, abs: p, kind: kindOf(rel), out: rel})
		return nil
	})
	return entries, err
}

func kindOf(name string) Kind {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case ext == ".md":
		return KindNote
	case imageExts[ext]:
		return KindImage
	default:
		return KindOther
	}
}

// placeImages переносит картинки в att. Картинки с одинаковым именем и
// одинаковым содержимым склеиваются в один файл, с разным содержимым —
// получают суффикс из хэша.
func placeImages(entries []*entry, att string) error {
	type slot struct {
		e   *entry
		sum string // вычисляется только при совпадении имён
	}
	taken := map[string]*slot{} // ключ в нижнем регистре: на macOS/Windows регистр в именах не различается

	for _, e := range entries {
		if e.kind != KindImage {
			continue
		}
		name := path.Base(e.rel)
		ext := path.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		var sum string

	candidates:
		for i := 0; ; i++ {
			var cand string
			switch i {
			case 0:
				cand = name
			case 1:
				cand = stem + "-" + sum[:8] + ext
			case 2:
				cand = stem + "-" + sum + ext
			default:
				return fmt.Errorf("не удалось подобрать имя для картинки %s", e.rel)
			}
			out := path.Join(att, cand)
			key := strings.ToLower(out)
			s, ok := taken[key]
			if !ok {
				e.out = out
				taken[key] = &slot{e: e, sum: sum}
				break candidates
			}

			var err error
			if sum == "" {
				if sum, err = fileSum(e.abs); err != nil {
					return err
				}
			}
			if s.sum == "" {
				if s.sum, err = fileSum(s.e.abs); err != nil {
					return err
				}
			}
			if s.sum == sum {
				e.out = s.e.out
				e.dup = true
				break candidates
			}
		}
	}
	return nil
}

func fileSum(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
