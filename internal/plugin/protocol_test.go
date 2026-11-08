package plugin

import (
	"bufio"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseHello(t *testing.T) {
	m, err := parseLine([]byte(`{"hello":{"name":"git","events":["prompt","cd"],"commands":[{"name":"gst","description":"git status"}],"complete":["git","gst"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &Hello{Name: "git", Events: []string{"prompt", "cd"}, Commands: []Command{{"gst", "git status"}}, Complete: []string{"git", "gst"}}
	if !reflect.DeepEqual(m.Hello, want) {
		t.Fatalf("got %+v, want %+v", m.Hello, want)
	}
	if m.ID != nil || len(m.actions("git")) != 0 {
		t.Fatalf("a hello is only a hello: %+v", m)
	}
}

func TestParseActions(t *testing.T) {
	cases := []struct {
		line string
		want []Action
	}{
		{`{"set":{"name":"x","value":["a","b"],"export":true}}`,
			[]Action{{Plugin: "p", Kind: ActSet, Name: "x", Values: []string{"a", "b"}, Export: true}}},
		{`{"set":{"name":"x","value":"one"}}`,
			[]Action{{Plugin: "p", Kind: ActSet, Name: "x", Values: []string{"one"}}}},
		{`{"set":{"name":"x"}}`,
			[]Action{{Plugin: "p", Kind: ActSet, Name: "x", Values: []string{}}}},
		{`{"unset":"x"}`, []Action{{Plugin: "p", Kind: ActUnset, Name: "x"}}},
		{`{"unset":["x","y"]}`, []Action{{Plugin: "p", Kind: ActUnset, Name: "x"}, {Plugin: "p", Kind: ActUnset, Name: "y"}}},
		{`{"message":"hi"}`, []Action{{Plugin: "p", Kind: ActMessage, Text: "hi"}}},
		{`{"segment":{"name":"git","text":"main +1"}}`, []Action{{Plugin: "p", Kind: ActSegment, Name: "git", Text: "main +1"}}},
		{`{"buffer":"git push"}`, []Action{{Plugin: "p", Kind: ActBuffer, Text: "git push"}}},
		{`{"cd":"/tmp"}`, []Action{{Plugin: "p", Kind: ActCd, Text: "/tmp"}}},
		{`{"eval":"abbr -a gs git status"}`, []Action{{Plugin: "p", Kind: ActEval, Text: "abbr -a gs git status"}}},
		// several in one line come out in a fixed order
		{`{"message":"m","eval":"e","cd":"/","set":{"name":"a","value":[]},"buffer":"b","segment":{"name":"s","text":"t"},"unset":"u"}`,
			[]Action{
				{Plugin: "p", Kind: ActSet, Name: "a", Values: []string{}},
				{Plugin: "p", Kind: ActUnset, Name: "u"},
				{Plugin: "p", Kind: ActSegment, Name: "s", Text: "t"},
				{Plugin: "p", Kind: ActCd, Text: "/"},
				{Plugin: "p", Kind: ActEval, Text: "e"},
				{Plugin: "p", Kind: ActBuffer, Text: "b"},
				{Plugin: "p", Kind: ActMessage, Text: "m"},
			}},
	}
	for _, c := range cases {
		m, err := parseLine([]byte(c.line))
		if err != nil {
			t.Errorf("%s: %v", c.line, err)
			continue
		}
		if got := m.actions("p"); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.line, got, c.want)
		}
	}
}

func TestParseReplies(t *testing.T) {
	m, err := parseLine([]byte(`{"id":7,"stdout":"out\n","stderr":"err\n","status":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == nil || *m.ID != 7 || m.Stdout != "out\n" || m.Stderr != "err\n" || m.Status != 3 {
		t.Fatalf("got %+v", m)
	}
	m, err = parseLine([]byte(`{"id":8,"candidates":[{"text":"apple","description":"a fruit"},{"text":"apricot"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{{"apple", "a fruit"}, {"apricot", ""}}
	if m.ID == nil || *m.ID != 8 || !reflect.DeepEqual(m.Candidates, want) {
		t.Fatalf("got %+v", m)
	}
	// no status means 0
	m, _ = parseLine([]byte(`{"id":9,"stdout":"x"}`))
	if m.Status != 0 {
		t.Fatalf("status = %d", m.Status)
	}
	// a value of the wrong type is an error, but the id is still there
	m, err = parseLine([]byte(`{"id":10,"status":"1"}`))
	if err == nil || m == nil || m.ID == nil || *m.ID != 10 {
		t.Fatalf("got %+v, %v", m, err)
	}
}

func TestParseBad(t *testing.T) {
	for _, l := range []string{`not json`, `[1,2]`, `{"set":{"name":"x","value":5}}`, `{"id":"seven"}`} {
		if _, err := parseLine([]byte(l)); err == nil {
			t.Errorf("%s: expected an error", l)
		}
	}
	m, err := parseLine([]byte(`{"unknown":1}`))
	if err != nil || !m.empty() {
		t.Errorf("unknown keys are ignored: %v %+v", err, m)
	}
}

func TestEncode(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{&promptEvent{Event: EventPrompt, Cwd: "/home/me", Status: 1, Pipestatus: []int{0, 1}, DurationMs: 1500},
			`{"event":"prompt","cwd":"/home/me","status":1,"pipestatus":[0,1],"duration_ms":1500}`},
		{&preexecEvent{Event: EventPreExec, Line: "make && ./run <in", Cwd: "/src"},
			`{"event":"preexec","line":"make && ./run <in","cwd":"/src"}`},
		{&postexecEvent{Event: EventPostExec, Line: "false", Status: 1, DurationMs: 3, Cwd: "/"},
			`{"event":"postexec","line":"false","status":1,"duration_ms":3,"cwd":"/"}`},
		{&cdEvent{Event: EventCd, From: "/a", To: "/b"}, `{"event":"cd","from":"/a","to":"/b"}`},
		{&exitEvent{Event: EventExit}, `{"event":"exit"}`},
		{&runRequest{ID: 1, Request: "run", Name: "gst", Args: []string{}, Cwd: "/x"},
			`{"id":1,"request":"run","name":"gst","args":[],"cwd":"/x"}`},
		{&runRequest{ID: 2, Request: "run", Name: "up", Args: []string{"a b"}, Cwd: "/x", Stdin: ptr("line\n")},
			`{"id":2,"request":"run","name":"up","args":["a b"],"cwd":"/x","stdin":"line\n"}`},
		{&completeRequest{ID: 3, Request: "complete", Name: "git", Line: "git ch", Cursor: 6, Cwd: "/src"},
			`{"id":3,"request":"complete","name":"git","line":"git ch","cursor":6,"cwd":"/src"}`},
		{&cancelRequest{ID: 4, Request: "cancel"}, `{"id":4,"request":"cancel"}`},
	}
	for _, c := range cases {
		if got := string(encode(c.v)); got != c.want+"\n" {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}

func ptr(s string) *string { return &s }

func TestReadLine(t *testing.T) {
	long := strings.Repeat("x", 300)
	in := "one\r\n" + long + "\ntwo\n" + strings.Repeat("y", 50) + "\nlast"
	r := bufio.NewReaderSize(strings.NewReader(in), 16)
	var got []string
	var errs []error
	for {
		l, err := readLine(r, 100)
		if err == errLong {
			errs = append(errs, err)
			continue
		}
		if len(l) > 0 {
			got = append(got, string(l))
		}
		if err != nil {
			break
		}
	}
	want := []string{"one", "two", strings.Repeat("y", 50), "last"}
	if !reflect.DeepEqual(got, want) || len(errs) != 1 {
		t.Fatalf("got %q (%d skipped), want %q (1 skipped)", got, len(errs), want)
	}
}

func TestNames(t *testing.T) {
	for _, n := range []string{"git", "git-status", "took.v2", "a_b", "x1"} {
		if !ValidName(n) {
			t.Errorf("%q should be a valid plugin name", n)
		}
	}
	for _, n := range []string{"", "-x", ".hidden", "a/b", "a b", "../x", "a:b", strings.Repeat("a", 65)} {
		if ValidName(n) {
			t.Errorf("%q should not be a valid plugin name", n)
		}
	}
	for _, n := range []string{"gst", "k8s:ctx", "git-up", "über"} {
		if !validCommand(n) {
			t.Errorf("%q should be a valid command", n)
		}
	}
	for _, n := range []string{"", "-x", "a/b", "a b", "a;b", "$x", "a*", "x=1"} {
		if validCommand(n) {
			t.Errorf("%q should not be a valid command", n)
		}
	}
	for _, n := range []string{"git", "Took_2"} {
		if !validSegment(n) {
			t.Errorf("%q should be a valid segment", n)
		}
	}
	for _, n := range []string{"", "git-status", "a b"} {
		if validSegment(n) {
			t.Errorf("%q should not be a valid segment", n)
		}
	}
}

func TestCompCache(t *testing.T) {
	var c compCache
	c.put("a", []Candidate{{Text: "x"}})
	if got, ok := c.get("a"); !ok || len(got) != 1 {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := c.get("b"); ok {
		t.Fatal("unexpected hit")
	}
	c.m["a"] = cacheEntry{cands: nil, at: time.Now().Add(-2 * cacheTTL)}
	if _, ok := c.get("a"); ok {
		t.Fatal("expired entries don't count")
	}
	for i := 0; i < 3*cacheMax; i++ {
		c.put(strings.Repeat("k", i+1), nil)
	}
	if len(c.m) > cacheMax {
		t.Fatalf("cache grew to %d", len(c.m))
	}
}
