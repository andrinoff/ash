package history

import (
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"ls -l", "ls -l"},
		{`a\b`, `a\\b`},
		{"a\tb", `a\tb`},
		{"for x in 1\n  echo $x\nend", `for x in 1\n  echo $x\nend`},
		{`printf '%s\n' x`, `printf '%s\\n' x`},
		{"\\\n", `\\\n`},
		{"\t\t", `\t\t`},
		{"héllo\twörld", `héllo\twörld`},
	}
	for _, tt := range tests {
		got := string(appendEscaped(nil, tt.in))
		if got != tt.want {
			t.Errorf("escape(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.ContainsAny(got, "\t\n") {
			t.Errorf("escape(%q) = %q contains a tab or newline", tt.in, got)
		}
		if back := unescape(got); back != tt.in {
			t.Errorf("unescape(%q) = %q, want %q", got, back, tt.in)
		}
	}
}

func TestUnescapeTolerant(t *testing.T) {
	tests := []struct{ in, want string }{
		{`a\xb`, `a\xb`},
		{`trailing\`, `trailing\`},
		{`\\\`, `\\`},
		{`\q\n`, "\\q\n"},
		{`\`, `\`},
	}
	for _, tt := range tests {
		if got := unescape(tt.in); got != tt.want {
			t.Errorf("unescape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		line string
		want item
		ok   bool
	}{
		{"1700000000\t0\t12\t/tmp\tls -l", item{cmd: "ls -l", dir: "/tmp", when: 1700000000, dur: 12}, true},
		{"1700000000\t-1\t0\t\techo", item{cmd: "echo", when: 1700000000, status: -1}, true},
		{"1700000000\t127\t5\t/a\\tb\tgit \\\\ x\\nfoo", item{cmd: "git \\ x\nfoo", dir: "/a\tb", when: 1700000000, dur: 5, status: 127}, true},
		{"0\t0\t0\t/\t:", item{cmd: ":", dir: "/"}, true},
		{"# ash history v1", item{}, false},
		{"", item{}, false},
		{"1700000000\t0\t12\t/tmp", item{}, false},
		{"1700000000\t0\t12\t/tmp\t", item{}, false},
		{"1700000000\t0\t12\t/tmp\tls\textra", item{}, false},
		{"17000x0000\t0\t12\t/tmp\tls", item{}, false},
		{"\t0\t0\t/\tls", item{}, false},
		{"1700000000\t0\t-5\t/tmp\tls", item{}, false},
		{"1700000000\t99999999999\t0\t/tmp\tls", item{}, false},
		{"1700000000\t0\t1234567890123456789\t/tmp\tls", item{}, false},
		{"garbage", item{}, false},
	}
	for _, tt := range tests {
		got, ok := parseLine(tt.line)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseLine(%q) = %+v, %v; want %+v, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRecordRoundTrip(t *testing.T) {
	items := []item{
		{cmd: "ls", dir: "/home/me", when: 1700000000, dur: 3, status: 0},
		{cmd: "for i in 1 2\n\techo $i\\n\nend", dir: "/tmp/a\tb", when: 1700000005, dur: 2000, status: 1},
		{cmd: `echo \\`, when: -5, status: -2},
	}
	var b []byte
	for i := range items {
		b = appendRecord(b, &items[i])
	}
	if got := strings.Count(string(b), "\n"); got != len(items) {
		t.Fatalf("%d lines for %d records:\n%s", got, len(items), b)
	}
	recs, n := parseRecords(header + string(b))
	if n != len(header)+len(b) {
		t.Errorf("consumed %d bytes, want %d", n, len(header)+len(b))
	}
	if len(recs) != len(items) {
		t.Fatalf("parsed %d records, want %d", len(recs), len(items))
	}
	for i := range items {
		if recs[i] != items[i] {
			t.Errorf("record %d: got %+v, want %+v", i, recs[i], items[i])
		}
	}
}

func TestParseRecordsSkipsPartialLine(t *testing.T) {
	data := header + "1\t0\t0\t/\ta\n" + "junk\n" + "2\t0\t0\t/\tb\n" + "3\t0\t0\t/\tc"
	recs, n := parseRecords(data)
	if want := len(data) - len("3\t0\t0\t/\tc"); n != want {
		t.Errorf("consumed %d bytes, want %d", n, want)
	}
	var cmds []string
	for _, r := range recs {
		cmds = append(cmds, r.cmd)
	}
	if got := strings.Join(cmds, ","); got != "a,b" {
		t.Errorf("got %s, want a,b", got)
	}
}

func TestTrimCommand(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ls", "ls"},
		{"ls   ", "ls"},
		{"ls \t\n", "ls"},
		{`echo foo\ `, `echo foo\ `},
		{`echo foo\\ `, `echo foo\\`},
		{`echo foo\\\ `, `echo foo\\\ `},
		{"   ", ""},
		{"", ""},
		{" ls ", " ls"},
		{"echo 'a  '", "echo 'a  '"},
	}
	for _, tt := range tests {
		if got := trimCommand(tt.in); got != tt.want {
			t.Errorf("trimCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHead(t *testing.T) {
	cmds := []string{"", "g", "git", "git status", "git stash pop", "ü", "\xff\xfe", "gi\x00t"}
	prefixes := []string{"", "g", "gi", "git", "git s", "git stat", "git statu", "git stash p", "ü", "\xff", "gi\x00", "x"}
	for _, c := range cmds {
		for _, p := range prefixes {
			want := strings.HasPrefix(c, p)
			got := headOf(c)&headMask(len(p)) == headOf(p)
			if want && !got {
				t.Errorf("head filter rejects %q for prefix %q", c, p)
			}
			if len(p) <= 8 && len(c) >= len(p) && got != want {
				t.Errorf("head filter for %q, prefix %q: %v, want %v", c, p, got, want)
			}
		}
	}
}
