package history

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseFish(t *testing.T) {
	data := `- cmd: echo hello
  when: 1700000000
- cmd: printf '%s\\n' a\\\\b
  when: 1700000001
  added_when: 1700000001
  paths:
    - a\\b
- cmd: for i in 1 2\n  echo $i\nend
  when: 1700000002
- cmd: - cmd: echo doubled
  when: 1700000003
- cmd:    when: 1700000004
- cmd: no time
- cmd: echo \x41
  when: 1700000005
%YAML 1.2
---
- cmd: echo hello
  when: 1700000006
- cmd: partial`
	want := []item{
		{cmd: "echo hello", when: 1700000000},
		{cmd: `printf '%s\n' a\\b`, when: 1700000001},
		{cmd: "for i in 1 2\n  echo $i\nend", when: 1700000002},
		{cmd: "echo doubled", when: 1700000003},
		{cmd: "no time"},
		{cmd: `echo \x41`, when: 1700000005},
		{cmd: "echo hello", when: 1700000006},
		{cmd: "partial"},
	}
	if got := parseFish(data); !reflect.DeepEqual(got, want) {
		t.Errorf("parseFish:\n got %+v\nwant %+v", got, want)
	}
}

func TestUnescapeFish(t *testing.T) {
	tests := []struct{ in, want string }{
		{`plain`, "plain"},
		{`a\nb`, "a\nb"},
		{`a\\nb`, `a\nb`},
		{`a\\\nb`, "a\\\nb"},
		{`\x\`, `\x\`},
		{`end\\`, `end\`},
	}
	for _, tt := range tests {
		if got := unescapeFish(tt.in); got != tt.want {
			t.Errorf("unescapeFish(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseZsh(t *testing.T) {
	data := ": 1700000000:0;echo hello\n" +
		": 1700000001:12;for i in 1 2\\\ndo\\\n  echo $i\\\ndone\n" +
		": 1700000002:0;echo trailing\\ \n" +
		"plain command\n" +
		"\\:colon start\n" +
		": 1700000003:0;echo \xc3\x83\x80\n" + // metafied "à"
		": abc:0;x\n" +
		"a\\\nb\n" +
		"last\\"
	want := []item{
		{cmd: "echo hello", when: 1700000000},
		{cmd: "for i in 1 2\ndo\n  echo $i\ndone", when: 1700000001, dur: 12000},
		{cmd: `echo trailing\`, when: 1700000002},
		{cmd: "plain command"},
		{cmd: ":colon start"},
		{cmd: "echo à", when: 1700000003},
		{cmd: ": abc:0;x"},
		{cmd: "a\nb"},
		{cmd: `last\`},
	}
	if got := parseZsh(data); !reflect.DeepEqual(got, want) {
		t.Errorf("parseZsh:\n got %+v\nwant %+v", got, want)
	}
}

func TestUnmetafy(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"\xe2\x80\x83\xb3", "\xe2\x80\x93"}, // en dash
		{"a\x83\x20b", "a\x00b"},             // NUL
		{"end\x83", "end\x83"},
	}
	for _, tt := range tests {
		if got := unmetafy(tt.in); got != tt.want {
			t.Errorf("unmetafy(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseBash(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []item
	}{
		{
			"timestamps",
			"#1700000000\necho one\n#1700000001\nfor i in 1 2\ndo echo $i\ndone\n#1700000002\necho three\r\n",
			[]item{
				{cmd: "echo one", when: 1700000000},
				{cmd: "for i in 1 2\ndo echo $i\ndone", when: 1700000001},
				{cmd: "echo three", when: 1700000002},
			},
		},
		{
			"plain",
			"ls -la\n# a comment\ncd /tmp\n\necho done\n#1700000005\necho timed\nnext",
			[]item{
				{cmd: "ls -la"},
				{cmd: "# a comment"},
				{cmd: "cd /tmp"},
				{cmd: "echo done"},
				{cmd: "echo timed", when: 1700000005},
				{cmd: "next"},
			},
		},
	}
	for _, tt := range tests {
		if got := parseBash(tt.data); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.name, got, tt.want)
		}
	}
}

func TestPrepareImport(t *testing.T) {
	tests := []struct {
		in    []item
		mtime int64
		want  []item
	}{
		{
			[]item{{cmd: "a"}, {cmd: "b", when: 100}, {cmd: "c"}, {cmd: "a", when: 300}, {cmd: " hidden", when: 50}, {cmd: "d  ", when: 200}, {cmd: "", when: 10}},
			999,
			[]item{{cmd: "b", when: 100}, {cmd: "c", when: 100}, {cmd: "d", when: 200}, {cmd: "a", when: 300}},
		},
		{
			[]item{{cmd: "x"}, {cmd: "y"}, {cmd: "x"}},
			500,
			[]item{{cmd: "y", when: 500}, {cmd: "x", when: 500}},
		},
		{
			[]item{{cmd: "late", when: 300}, {cmd: "early", when: 100}},
			0,
			[]item{{cmd: "early", when: 100}, {cmd: "late", when: 300}},
		},
	}
	for _, tt := range tests {
		if got := prepareImport(tt.in, tt.mtime); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("prepareImport:\n got %+v\nwant %+v", got, tt.want)
		}
	}
}

func TestImportFile(t *testing.T) {
	s, path := open(t)
	if err := s.Add(Entry{Cmd: "ls", When: time.Unix(1700000100, 0), Dir: "/a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Entry{Cmd: "make", When: time.Unix(1700000300, 0), Status: 2}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "fish_history")
	writeFile(t, src, "- cmd: echo hello\n  when: 1700000000\n"+
		"- cmd: make\n  when: 1700000050\n"+
		"- cmd: cd /tmp\n  when: 1700000200\n"+
		"- cmd: echo hello\n  when: 1700000250\n"+
		"- cmd: git log\n  when: 1700000400\n")
	n, err := s.ImportFile("fish", src)
	if n != 3 || err != nil {
		t.Fatalf("ImportFile = %d, %v; want 3", n, err)
	}
	want := []string{"git log", "make", "echo hello", "cd /tmp", "ls"}
	equal(t, "Recent", s.Recent(0), want)
	wantFile := header +
		"1700000100\t0\t0\t/a\tls\n" +
		"1700000200\t0\t0\t\tcd /tmp\n" +
		"1700000250\t0\t0\t\techo hello\n" +
		"1700000300\t2\t0\t\tmake\n" +
		"1700000400\t0\t0\t\tgit log\n"
	if got := readFile(t, path); got != wantFile {
		t.Errorf("file:\n%s\nwant:\n%s", got, wantFile)
	}
	equal(t, "reopened", reopen(t, path).Recent(0), want)
	if n, err := s.ImportFile("fish", src); n != 0 || err != nil {
		t.Errorf("importing again = %d, %v", n, err)
	}
	if got := readFile(t, path); got != wantFile {
		t.Errorf("importing again changed the file:\n%s", got)
	}
	add(t, s, "after")
	equal(t, "after adding", reopen(t, path).Recent(2), []string{"after", "git log"})
}

func TestImportKinds(t *testing.T) {
	mtime := time.Unix(1700000500, 0)
	tests := []struct {
		kind, data string
		want       []Entry
	}{
		{
			"zsh",
			": 1700000000:0;echo hello\n: 1700000001:12;for i in 1 2\\\ndo\\\n  echo $i\\\ndone\nplain command\n: 1700000003:0;echo \xc3\x83\x80\n",
			[]Entry{
				{Cmd: "echo à", When: time.Unix(1700000003, 0)},
				{Cmd: "plain command", When: time.Unix(1700000001, 0)},
				{Cmd: "for i in 1 2\ndo\n  echo $i\ndone", When: time.Unix(1700000001, 0), Duration: 12 * time.Second},
				{Cmd: "echo hello", When: time.Unix(1700000000, 0)},
			},
		},
		{
			"bash",
			"ls -la\ncd /tmp\nls -la\necho done\n",
			[]Entry{
				{Cmd: "echo done", When: mtime},
				{Cmd: "ls -la", When: mtime},
				{Cmd: "cd /tmp", When: mtime},
			},
		},
		{
			"bash",
			"#1700000000\necho one\n#1700000001\nfor i in 1 2\ndo echo $i\ndone\n",
			[]Entry{
				{Cmd: "for i in 1 2\ndo echo $i\ndone", When: time.Unix(1700000001, 0)},
				{Cmd: "echo one", When: time.Unix(1700000000, 0)},
			},
		},
	}
	for _, tt := range tests {
		src := filepath.Join(t.TempDir(), tt.kind+"_history")
		writeFile(t, src, tt.data)
		if err := os.Chtimes(src, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		s, path := open(t)
		n, err := s.ImportFile(tt.kind, src)
		if n != len(tt.want) || err != nil {
			t.Errorf("%s: ImportFile = %d, %v; want %d", tt.kind, n, err, len(tt.want))
		}
		for _, st := range []*Store{s, reopen(t, path)} {
			if got := st.Entries(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s: entries\n got %+v\nwant %+v", tt.kind, got, tt.want)
			}
		}
	}
}

func TestImportErrors(t *testing.T) {
	s, path := open(t)
	if _, err := s.ImportFile("csh", "/dev/null"); err == nil || !strings.Contains(err.Error(), "csh") {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := s.Import("tcsh"); err == nil {
		t.Error("no error for an unknown kind")
	}
	if _, err := s.ImportFile("zsh", filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := s.ImportFile("bash", t.TempDir()); err == nil {
		t.Error("no error importing a directory")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("failed imports created the history file (%v)", err)
	}
}

func TestImportMemoryOnly(t *testing.T) {
	s := Open("")
	add(t, s, "newer")
	src := filepath.Join(t.TempDir(), "bash_history")
	writeFile(t, src, "#1600000000\nolder\n#1800000000\nnewest\n")
	if n, err := s.ImportFile("bash", src); n != 2 || err != nil {
		t.Errorf("ImportFile = %d, %v", n, err)
	}
	equal(t, "Recent", s.Recent(0), []string{"newest", "newer", "older"})
}

func TestSourcePath(t *testing.T) {
	tests := []struct {
		kind string
		env  map[string]string
		want string
	}{
		{"fish", map[string]string{"HOME": "/home/u"}, "/home/u/.local/share/fish/fish_history"},
		{"fish", map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "/data"}, "/data/fish/fish_history"},
		{"fish", map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "relative"}, "/home/u/.local/share/fish/fish_history"},
		{"zsh", map[string]string{"HOME": "/home/u"}, "/home/u/.zsh_history"},
		{"zsh", map[string]string{"HOME": "/home/u", "ZDOTDIR": "/zdot"}, "/zdot/.zsh_history"},
		{"zsh", map[string]string{"HOME": "/home/u", "ZDOTDIR": "/zdot", "HISTFILE": "/h/zhist"}, "/h/zhist"},
		{"bash", map[string]string{"HOME": "/home/u", "HISTFILE": "/h/zhist"}, "/home/u/.bash_history"},
	}
	for _, tt := range tests {
		got, err := SourcePath(tt.kind, func(k string) string { return tt.env[k] })
		if got != tt.want || err != nil {
			t.Errorf("SourcePath(%s, %v) = %q, %v; want %q", tt.kind, tt.env, got, err, tt.want)
		}
	}
	if _, err := SourcePath("csh", os.Getenv); err == nil {
		t.Error("no error for an unknown kind")
	}
}

func TestAutoImport(t *testing.T) {
	sources := map[string]struct{ rel, data string }{
		"fish": {"xdg/fish/fish_history", "- cmd: from fish\n  when: 1700000000\n"},
		"zsh":  {"home/.zsh_history", ": 1700000000:0;from zsh\n"},
		"bash": {"home/.bash_history", "#1700000000\nfrom bash\n"},
	}
	tests := []struct {
		name    string
		present []string
		empty   string // a source that exists but is empty
		want    []string
	}{
		{"all", []string{"fish", "zsh", "bash"}, "", []string{"from fish"}},
		{"no fish", []string{"zsh", "bash"}, "", []string{"from zsh"}},
		{"only bash", []string{"bash"}, "", []string{"from bash"}},
		{"empty fish", []string{"zsh"}, "fish", []string{"from zsh"}},
		{"none", nil, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", filepath.Join(dir, "home"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
			t.Setenv("HISTFILE", "")
			t.Setenv("ZDOTDIR", "")
			for _, k := range tt.present {
				writeFile(t, filepath.Join(dir, sources[k].rel), sources[k].data)
			}
			if tt.empty != "" {
				writeFile(t, filepath.Join(dir, sources[tt.empty].rel), "")
			}
			path := filepath.Join(dir, "xdg", "ash", "history")
			s := Open(path)
			n, err := s.AutoImport()
			if n != len(tt.want) || err != nil {
				t.Errorf("AutoImport = %d, %v; want %d", n, err, len(tt.want))
			}
			equal(t, "Recent", s.Recent(0), tt.want)
			if n, err := s.AutoImport(); n != 0 || err != nil {
				t.Errorf("second AutoImport = %d, %v", n, err)
			}
			if _, err := os.Stat(path); (err == nil) != (len(tt.want) > 0) {
				t.Errorf("history file exists: %v", err == nil)
			}
		})
	}

	t.Run("existing history", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", filepath.Join(dir, "home"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
		writeFile(t, filepath.Join(dir, sources["fish"].rel), sources["fish"].data)
		path := filepath.Join(dir, "ash_history")
		writeFile(t, path, header+"1\t0\t0\t\tmine\n")
		s := Open(path)
		if n, err := s.AutoImport(); n != 0 || err != nil {
			t.Errorf("AutoImport = %d, %v", n, err)
		}
		equal(t, "Recent", s.Recent(0), []string{"mine"})
	})

	t.Run("after clear", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", filepath.Join(dir, "home"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
		writeFile(t, filepath.Join(dir, sources["fish"].rel), sources["fish"].data)
		s := Open(filepath.Join(dir, "ash_history"))
		s.Clear()
		if n, err := s.AutoImport(); n != 0 || err != nil || s.Len() != 0 {
			t.Errorf("AutoImport after Clear = %d, %v", n, err)
		}
		mem := Open("")
		if n, err := mem.AutoImport(); n != 0 || err != nil {
			t.Errorf("AutoImport into memory = %d, %v", n, err)
		}
	})
}

// TestSmokeFishHistory imports the user's real fish history (which it only
// reads) into a temporary file. It logs counts, never commands.
func TestSmokeFishHistory(t *testing.T) {
	if os.Getenv("ASH_HISTORY_SMOKE") == "" {
		t.Skip("set ASH_HISTORY_SMOKE=1 to import the real fish history")
	}
	src, err := SourcePath("fish", os.Getenv)
	if err != nil {
		t.Skip(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skip(err)
	}
	path := filepath.Join(t.TempDir(), "history")
	s := Open(path)
	start := time.Now()
	n, err := s.ImportFile("fish", src)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	r := reopen(t, path)
	multi, untimed := 0, 0
	for _, e := range r.Entries() {
		if strings.Contains(e.Cmd, "\n") {
			multi++
		}
		if e.When.Unix() < 1e9 {
			untimed++
		}
	}
	t.Logf("%d \"- cmd:\" lines; imported %d commands in %v; %d after reopening, %d multi-line, %d without a time",
		strings.Count("\n"+string(data), "\n- cmd:"), n, took, r.Len(), multi, untimed)
	if n == 0 || r.Len() != n {
		t.Errorf("imported %d, reopened %d", n, r.Len())
	}
}
