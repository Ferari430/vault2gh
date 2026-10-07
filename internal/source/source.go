// Package source готовит хранилище к обработке: папку берёт как есть,
// zip-архив безопасно распаковывает во временную папку.
package source

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Ограничения на распаковку — защита от zip-бомб.
var (
	maxFiles    = 50_000
	maxUnpacked = int64(2 << 30) // 2 ГБ
)

// Open возвращает папку хранилища для input (папка или .zip) и функцию,
// удаляющую временные файлы.
func Open(input string) (root string, cleanup func(), err error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		return input, func() {}, nil
	}
	if !strings.EqualFold(filepath.Ext(input), ".zip") {
		return "", nil, fmt.Errorf("%s: ожидается папка хранилища или .zip-архив", input)
	}

	tmp, err := os.MkdirTemp("", "vault2gh-unzip-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(tmp) }
	if err := Unzip(input, tmp); err != nil {
		cleanup()
		return "", nil, err
	}
	return vaultRoot(tmp), cleanup, nil
}

// vaultRoot спускается внутрь, пока в папке ровно одна подпапка и нет файлов:
// архив «MyVault.zip → MyVault/…» и архив с заметками в корне дают одно и то же.
func vaultRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".obsidian")); err == nil {
			return dir
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return dir
		}
		var only string
		visible := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || e.Name() == "__MACOSX" {
				continue
			}
			visible++
			if e.IsDir() {
				only = e.Name()
			}
		}
		if visible != 1 || only == "" {
			return dir
		}
		dir = filepath.Join(dir, only)
	}
}

// Unzip распаковывает src в dst. Пути, выходящие за dst, симлинки и
// служебные папки macOS пропускаются или отклоняются.
func Unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("не удалось открыть архив: %w", err)
	}
	defer r.Close()
	if len(r.File) > maxFiles {
		return fmt.Errorf("в архиве больше %d файлов", maxFiles)
	}

	var total int64
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, `\`, "/") // архиваторы Windows иногда пишут обратные слэши
		if strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		local := filepath.FromSlash(strings.TrimSuffix(name, "/"))
		if !filepath.IsLocal(local) {
			return fmt.Errorf("недопустимый путь в архиве: %q", f.Name)
		}
		path := filepath.Join(dst, local)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		n, err := extract(f, path, maxUnpacked-total)
		if err != nil {
			return err
		}
		total += n
	}
	return nil
}

var errTooBig = errors.New("архив слишком большой после распаковки")

func extract(f *zip.File, dst string, limit int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("%s: %w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	// Размер из заголовка архива может врать, поэтому считаем реально записанное.
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("%s: %w", f.Name, err)
	}
	if n > limit {
		return n, errTooBig
	}
	return n, nil
}
