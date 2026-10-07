package vault

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeVault(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func build(t *testing.T, files map[string]string) (*Result, map[string]string) {
	t.Helper()
	res, err := Build(writeVault(t, files), Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := res.Export(out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	err = filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		data, err := os.ReadFile(p)
		got[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res, got
}

func TestRewriteLinks(t *testing.T) {
	vaultFiles := map[string]string{
		"img/a b.png":              "A",
		"img/pic.png":              "P",
		"img/скрин (1).png":        "S",
		"docs/file.pdf":            "PDF",
		"Notes/Other note.md":      "# Other",
		"Notes/Deep/Leaf.md":       "leaf",
		".obsidian/workspace.json": "{}",
	}
	tests := []struct {
		name string
		note string // путь заметки в хранилище
		in   string
		want string
	}{
		{"embed from root", "root.md", "![[a b.png]]", "![a b](attachments/a%20b.png)"},
		{"embed from subfolder", "Notes/n.md", "![[pic.png]]", "![pic](../attachments/pic.png)"},
		{"embed width", "root.md", "![[pic.png|300]]", `<img src="attachments/pic.png" alt="pic" width="300">`},
		{"embed width and height", "root.md", "![[pic.png|300x200]]", `<img src="attachments/pic.png" alt="pic" width="300" height="200">`},
		{"embed with caption", "root.md", "![[pic.png|Схема]]", "![Схема](attachments/pic.png)"},
		{"embed with folder", "root.md", "![[img/pic.png]]", "![pic](attachments/pic.png)"},
		{"cyrillic and parens", "root.md", "![[скрин (1).png]]", "![скрин (1)](attachments/скрин%20%281%29.png)"},
		{"markdown relative", "Notes/n.md", "![x](../img/pic.png)", "![x](../attachments/pic.png)"},
		{"markdown vault-root path", "Notes/n.md", "![x](img/pic.png)", "![x](../attachments/pic.png)"},
		{"markdown encoded", "root.md", "![](a%20b.png)", "![](attachments/a%20b.png)"},
		{"markdown angle brackets", "root.md", "![x](<img/a b.png>)", "![x](attachments/a%20b.png)"},
		{"markdown title kept", "root.md", `![x](pic.png "t")`, `![x](attachments/pic.png "t")`},
		{"absolute local path", "root.md", "![](/home/user/pics/pic.png)", "![](attachments/pic.png)"},
		{"url untouched", "root.md", "![x](https://example.com/a.png) [y](mailto:a@b.c)", "![x](https://example.com/a.png) [y](mailto:a@b.c)"},
		{"wiki note link", "root.md", "[[Other note]]", "[Other note](Notes/Other%20note.md)"},
		{"wiki link from sibling dir", "Notes/Deep/Leaf2.md", "[[Other note]]", "[Other note](../Other%20note.md)"},
		{"wiki heading and alias", "root.md", "[[Other note#Мой Заголовок!|см.]]", "[см.](Notes/Other%20note.md#мой-заголовок)"},
		{"wiki heading no alias", "root.md", "[[Leaf#Part 2]]", "[Leaf > Part 2](Notes/Deep/Leaf.md#part-2)"},
		{"wiki block ref", "root.md", "[[Leaf#^abc123]]", "[Leaf](Notes/Deep/Leaf.md)"},
		{"wiki same note heading", "root.md", "[[#Intro Part]]", "[Intro Part](#intro-part)"},
		{"embed note becomes link", "root.md", "![[Leaf]]", "[Leaf](Notes/Deep/Leaf.md)"},
		{"embed pdf becomes link", "root.md", "![[file.pdf]]", "[file.pdf](docs/file.pdf)"},
		{"table escaped pipe", "root.md", `| ![[pic.png\|100]] |`, `| <img src="attachments/pic.png" alt="pic" width="100"> |`},
		{"markdown note link fixed", "Notes/Deep/Leaf2.md", "[o](Notes/Other%20note.md#My%20Head)", "[o](../Other%20note.md#my-head)"},
		{"image inside link", "root.md", "[![](pic.png)](Leaf.md)", "[![](attachments/pic.png)](Notes/Deep/Leaf.md)"},
		{"two on one line", "root.md", "a ![[pic.png]] b ![[a b.png]]", "a ![pic](attachments/pic.png) b ![a b](attachments/a%20b.png)"},
		{"unresolved kept", "root.md", "![[missing.png]] [[Nope]]", "![[missing.png]] [[Nope]]"},
		{"inline code kept", "root.md", "`![[pic.png]]` and ![[pic.png]]", "`![[pic.png]]` and ![pic](attachments/pic.png)"},
		{"double backtick code kept", "root.md", "``a ` ![[pic.png]]`` x", "``a ` ![[pic.png]]`` x"},
		{"fenced code kept", "root.md", "```md\n![[pic.png]]\n```\n![[pic.png]]", "```md\n![[pic.png]]\n```\n![pic](attachments/pic.png)"},
		{"tilde fence in quote", "root.md", "> ~~~\n> ![[pic.png]]\n> ~~~", "> ~~~\n> ![[pic.png]]\n> ~~~"},
		{"front matter kept", "root.md", "---\ncover: \"[[pic.png]]\"\n---\n![[pic.png]]", "---\ncover: \"[[pic.png]]\"\n---\n![pic](attachments/pic.png)"},
		{"crlf preserved", "root.md", "![[pic.png]]\r\nx\r\n", "![pic](attachments/pic.png)\r\nx\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{tt.note: tt.in}
			maps.Copy(files, vaultFiles)
			_, got := build(t, files)
			if got[tt.note] != tt.want {
				t.Errorf("\n in:   %q\n got:  %q\n want: %q", tt.in, got[tt.note], tt.want)
			}
		})
	}
}

func TestLayout(t *testing.T) {
	res, got := build(t, map[string]string{
		"n.md":               "![[x.png]]",
		"a/x.png":            "one",
		"b/x.png":            "two", // то же имя, другое содержимое
		"c/X.png":            "one", // то же имя без учёта регистра и то же содержимое — дубликат a/x.png
		"d/y.png":            "y",
		"files/doc.pdf":      "pdf",
		".obsidian/app.json": "{}",
		".trash/old.md":      "old",
		"sub/.hidden.md":     "h",
	})
	var paths []string
	for p := range got {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	want := []string{"attachments/x-3fc4ccfe.png", "attachments/x.png", "attachments/y.png", "files/doc.pdf", "n.md"}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths:\n got  %v\n want %v", paths, want)
	}
	if got["attachments/x.png"] != "one" || got["attachments/x-3fc4ccfe.png"] != "two" {
		t.Errorf("collision handled wrong: x.png=%q x-…=%q", got["attachments/x.png"], got["attachments/x-3fc4ccfe.png"])
	}
	if res.Stats != (Stats{Notes: 1, Images: 3, Other: 1}) {
		t.Errorf("stats = %+v", res.Stats)
	}
}

func TestCollisionResolvesToNearestImage(t *testing.T) {
	_, got := build(t, map[string]string{
		"a/x.png":   "one",
		"b/x.png":   "two",
		"b/note.md": "![[x.png]] ![[a/x.png]]",
	})
	want := "![x](../attachments/x-3fc4ccfe.png) ![x](../attachments/x.png)"
	if got["b/note.md"] != want {
		t.Errorf("got %q, want %q", got["b/note.md"], want)
	}
}

func TestUnresolvedReported(t *testing.T) {
	res, _ := build(t, map[string]string{
		"n.md": "![[gone.png]] ![[gone.png]] [x](missing.md)",
	})
	want := []Unresolved{{"n.md", "![[gone.png]]"}, {"n.md", "[x](missing.md)"}}
	if !slices.Equal(res.Unresolved, want) {
		t.Errorf("got %v, want %v", res.Unresolved, want)
	}
}

func TestAttachmentsDirValidation(t *testing.T) {
	root := writeVault(t, map[string]string{"n.md": ""})
	for _, bad := range []string{"../x", "/abs", ".", ".git"} {
		if _, err := Build(root, Options{AttachmentsDir: bad}); err == nil {
			t.Errorf("AttachmentsDir %q: expected error", bad)
		}
	}
	res, err := Build(writeVault(t, map[string]string{"n.md": "![[p.png]]", "p.png": "p"}), Options{AttachmentsDir: "assets/img/"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Files[0].data), "](assets/img/p.png)") {
		t.Errorf("custom dir not used: %q", res.Files[0].data)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Hello World":         "hello-world",
		"Мой Заголовок!":      "мой-заголовок",
		"C++ & Go: 2 языка":   "c--go-2-языка",
		"snake_case **bold**": "snake_case-bold",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
