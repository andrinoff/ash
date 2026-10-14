package editor

import "testing"

func TestCommonPrefix(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"foo", "foobar", "foo"},
		{"foobar", "foo", "foo"},
		{"foo", "foo", "foo"},
		{"file0", "file02.txt", "file0"},
		{"", "x", ""},
		{"héllo", "hêllo", "h"},
		{"日本", "日本語", "日本"},
	}
	for _, c := range cases {
		if got := commonPrefix(c.a, c.b); got != c.want {
			t.Errorf("commonPrefix(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// Completing with candidates where one is a prefix of another used to
// panic while computing the common prefix.
func TestCompleteNestedCandidates(t *testing.T) {
	e := newTestEditor(80)
	e.cfg.Complete = func(buf string, cursor int) Completion {
		return Completion{Start: 3, End: cursor, Items: []Candidate{
			{Text: "file00.txt"}, {Text: "file01.txt"}, {Text: "file02.txt"},
		}}
	}
	e.insert([]rune("ls f"))
	e.complete(false)
	if got := string(e.buf); got != "ls file0" {
		t.Fatalf("common prefix not inserted: %q", got)
	}
	e.cfg.Complete = func(buf string, cursor int) Completion {
		return Completion{Start: 0, End: cursor, Items: []Candidate{{Text: "git"}, {Text: "git-lfs"}}}
	}
	e.buf, e.pos = []rune("gi"), 2
	e.complete(false)
	if got := string(e.buf); got != "git" {
		t.Fatalf("got %q", got)
	}
	e.complete(false)
	if e.pager == nil {
		t.Fatal("second tab should open the pager")
	}
}
