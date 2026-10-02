package shell

import (
	"reflect"
	"testing"

	"github.com/andrinoff/ash/internal/syntax"
)

func TestBraceSeqLimits(t *testing.T) {
	for _, c := range []struct {
		start, end, step string
		want             []string
	}{
		{"1", "10", "4", []string{"1", "5", "9"}},
		{"5", "1", "2", []string{"5", "3", "1"}},
		{"a", "e", "2", []string{"a", "c", "e"}},
		{"9223372036854775806", "9223372036854775807", "", []string{"9223372036854775806", "9223372036854775807"}},
		{"-9223372036854775807", "-9223372036854775808", "", []string{"-9223372036854775807", "-9223372036854775808"}},
		{"1", "3", "9223372036854775807", []string{"1"}},
		{"1", "99999999999999999999", "", []string{"{1..99999999999999999999}"}},
	} {
		got := expandSeq(&syntax.BraceSeq{Start: c.start, End: c.end, Step: c.step})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("{%s..%s..%s} = %q (%d items), want %q", c.start, c.end, c.step, truncList(got), len(got), c.want)
		}
	}
}

func TestTildeAfterBraceExpansion(t *testing.T) {
	sh := New()
	home := sh.HomeDir()
	for _, c := range []struct {
		src  string
		want []string
	}{
		{`echo ~{,/x}`, []string{"echo", home, home + "/x"}},
		{`echo ~{/a,/b}/c`, []string{"echo", home + "/a/c", home + "/b/c"}},
		{`echo ~{'',/q}`, []string{"echo", "~", home + "/q"}},
		{`echo a~{,x}`, []string{"echo", "a~", "a~x"}},
		{`echo ~nosuchuser{1,2}`, []string{"echo", "~nosuchuser1", "~nosuchuser2"}},
	} {
		f, err := syntax.Parse(c.src)
		if err != nil {
			t.Fatal(err)
		}
		got, err := sh.expandFields(f.Stmts[0].Cmd.(*syntax.CallExpr).Args, sh.StdIO(), 0)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q %v, want %q", c.src, got, err, c.want)
		}
	}
}

func truncList(s []string) []string {
	if len(s) > 5 {
		return s[:5]
	}
	return s
}
