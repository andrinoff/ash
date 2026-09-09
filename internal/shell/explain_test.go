package shell

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func explainShell(t *testing.T) (*Shell, string) {
	t.Helper()
	sh, work := newCompShell(t)
	run(t, sh, `
function frob --description 'Frobnicate things'
end
complete -c frob -s x -d Extract
complete -c frob -s v -d Verbose
complete -c frob -s f -r -d 'Archive file'
complete -c frob -l level -r -d 'Compression level'
complete -c frob -o name -r -d 'Match a name'
complete -c frob -f -a "build\t'Compile it'" -a "test\t'Run the tests'"
complete -c frob -n 'contains build (commandline -opc)' -s r -d 'Release mode'
complete -c frob -n 'contains test (commandline -opc)' -s r -d 'Race detector'
complete -c sudo -s u -r -d 'Run as user'
`)
	touch(t, work, "src/")
	sudo := filepath.Join(sh.Get("PATH"), "sudo")
	if err := os.WriteFile(sudo, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return sh, work
}

func TestExplain(t *testing.T) {
	sh, _ := explainShell(t)
	type row = ExplainRow
	cmd := row{Word: "frob", Desc: "Frobnicate things", Kind: "command"}
	opt := func(w, d string) row { return row{Word: w, Desc: d, Kind: "option", Indent: 1} }
	val := func(w, of string) row { return row{Word: w, Desc: "argument of " + of, Kind: "value", Indent: 1} }
	arg := func(w, d string) row { return row{Word: w, Desc: d, Kind: "arg", Indent: 1} }
	tests := []struct {
		line string
		want []row
	}{
		{"frob -xvf a.tgz", []row{cmd, opt("-x", "Extract"), opt("-v", "Verbose"), opt("-f", "Archive file"), val("a.tgz", "-f")}},
		{"frob -fa.tgz", []row{cmd, opt("-f", "Archive file"), val("a.tgz", "-f")}},
		{"frob --level 9 --level=3", []row{cmd, opt("--level", "Compression level"), val("9", "--level"),
			opt("--level", "Compression level"), val("3", "--level")}},
		{"frob -name '*.go'", []row{cmd, opt("-name", "Match a name"), val("'*.go'", "-name")}},
		// subcommands, and options whose meaning depends on them
		{"frob build -r", []row{cmd, arg("build", "Compile it"), opt("-r", "Release mode")}},
		{"frob test -r", []row{cmd, arg("test", "Run the tests"), opt("-r", "Race detector")}},
		{"frob -- -x src", []row{cmd, opt("--", "end of options"), arg("-x", ""), arg("src", "directory")}},
		{"frob -q", []row{cmd, opt("-q", "")}},
		// a command that runs another
		{"sudo -u root frob -x", []row{{Word: "sudo", Desc: "/usr/bin/sudo", Kind: "command"}, opt("-u", "Run as user"),
			val("root", "-u"), cmd, opt("-x", "Extract")}},
		{"A=1 frob | nosuchcmd-ash", []row{{Word: "A=1", Desc: "set A for this command", Kind: "assign"}, cmd,
			{Word: "nosuchcmd-ash", Desc: "not found", Kind: "missing"}}},
		// an unfinished line still gets explained
		{"frob -f 'my file", []row{cmd, opt("-f", "Archive file"), val("'my file", "-f")}},
		{"echo $HOME", []row{{Word: "echo", Desc: builtinHelp["echo"], Kind: "command"}, arg("$HOME", "")}},
		{"", nil},
	}
	for _, tt := range tests {
		got := sh.Explain(tt.line)
		if strings.HasPrefix(tt.line, "sudo") {
			if len(got) == 0 || got[0].Word != "sudo" {
				t.Errorf("explain %q: got %+v", tt.line, got)
				continue
			}
			got[0].Desc = "/usr/bin/sudo" // the man page summary differs by system
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("explain %q:\n got %+v\nwant %+v", tt.line, got, tt.want)
		}
	}
}

func TestExplainBuiltin(t *testing.T) {
	sh, _ := explainShell(t)
	out := run(t, sh, `explain frob -xf a.tgz`)
	want := "frob     Frobnicate things\n" +
		"  -x     Extract\n" +
		"  -f     Archive file\n" +
		"  a.tgz  argument of -f\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if out := run(t, sh, `echo 'frob build' | explain`); !strings.Contains(out, "build  Compile it") {
		t.Errorf("from standard input: %q", out)
	}
}

func TestManSummary(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if strings.HasSuffix(name, ".gz") {
			f, err := os.Create(p)
			if err != nil {
				t.Fatal(err)
			}
			z := gzip.NewWriter(f)
			z.Write([]byte(content))
			z.Close()
			f.Close()
			return p
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name, content, want string
	}{
		{"man1/mdoc.1", ".Dd May 1\n.Dt MDOC 1\n.Sh NAME\n.Nm mdoc\n.Nd manipulate \\&tape archives\n.Sh SYNOPSIS\n", "manipulate tape archives"},
		{"man1/plain.1", ".TH PLAIN 1\n.SH \"NAME\"\nplain \\- the \\fBstupid\\fR content tracker\n.SH \"SYNOPSIS\"\n", "the stupid content tracker"},
		{"man1/wrapped.1.gz", ".TH W 1\n.SH NAME\nwrapped, wr \\- a summary\nthat goes on\n.SH DESCRIPTION\n", "a summary that goes on"},
		{"man1/alias.1", ".so man1/plain.1\n", "the stupid content tracker"},
		{"man1/none.1", ".TH NONE 1\n.SH DESCRIPTION\nnothing here\n", ""},
	}
	for _, tt := range tests {
		p := write(tt.name, tt.content)
		if got := readManSummary(p, 0); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}

	// found next to the program, the way man looks for pages
	bin := filepath.Join(dir, "prefix", "bin")
	os.MkdirAll(bin, 0o755)
	write("prefix/share/man/man1/tool-ash-test.1", ".SH NAME\ntool-ash-test \\- does a thing\n")
	sh := New()
	sh.Init()
	if got := sh.manSummary("tool-ash-test", filepath.Join(bin, "tool-ash-test")); got != "does a thing" {
		t.Errorf("next to the program: got %q", got)
	}
}
