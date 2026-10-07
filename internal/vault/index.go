package vault

import (
	"path"
	"strings"
)

type index struct {
	byPath map[string]*entry   // путь в хранилище в нижнем регистре
	byName map[string][]*entry // имя файла в нижнем регистре
}

func newIndex(entries []*entry) *index {
	ix := &index{byPath: map[string]*entry{}, byName: map[string][]*entry{}}
	for _, e := range entries {
		key := strings.ToLower(e.rel)
		if _, ok := ix.byPath[key]; !ok {
			ix.byPath[key] = e
		}
		name := strings.ToLower(path.Base(e.rel))
		ix.byName[name] = append(ix.byName[name], e)
	}
	return ix
}

// resolve ищет файл так же, как Obsidian: сначала по пути относительно заметки
// from или корня хранилища, затем по имени в любом месте хранилища. Расширение
// .md у заметок можно не указывать.
func (ix *index) resolve(target, from string) *entry {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}

	var paths []string
	if strings.HasPrefix(target, "/") {
		paths = []string{path.Clean(strings.TrimLeft(target, "/"))}
	} else {
		paths = []string{path.Join(path.Dir(from), target), path.Clean(target)}
	}
	for _, p := range paths {
		for _, cand := range []string{p, p + ".md"} {
			if e := ix.byPath[strings.ToLower(cand)]; e != nil {
				return e
			}
		}
	}

	// Несколько файлов с одним именем: предпочитаем тот, чей путь
	// заканчивается на target, затем лежащий рядом с заметкой, затем
	// ближайший к корню.
	name := strings.ToLower(path.Base(target))
	matches := append(append([]*entry(nil), ix.byName[name]...), ix.byName[name+".md"]...)
	want := "/" + strings.ToLower(strings.TrimLeft(path.Clean(target), "/"))
	dir := path.Dir(from)

	var best *entry
	bestScore := 0
	for _, e := range matches {
		score := 0
		rel := "/" + strings.ToLower(e.rel)
		if strings.HasSuffix(rel, want) || strings.HasSuffix(strings.TrimSuffix(rel, ".md"), want) {
			score += 1000
		}
		if path.Dir(e.rel) == dir {
			score += 100
		}
		score -= strings.Count(e.rel, "/")
		if best == nil || score > bestScore {
			best, bestScore = e, score
		}
	}
	return best
}
