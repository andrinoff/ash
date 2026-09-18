package shell

import (
	"reflect"
	"strings"
	"testing"
)

func TestScanCompLine(t *testing.T) {
	type want struct {
		words    []string // values of the words of the current command
		tok      string   // value of the token
		raw      string   // source of the token
		cmd      int      // commandIndex
		quote    byte
		escape   bool
		varName  string // text from varPos to the cursor, "-" for none
		brace    bool
		redirect bool
		skip     bool
	}
	tests := []struct {
		line string
		want want
	}{
		{"", want{tok: "", cmd: -1, varName: "-"}},
		{"gi", want{tok: "gi", raw: "gi", cmd: -1, varName: "-"}},
		{"git che", want{words: []string{"git"}, tok: "che", raw: "che", varName: "-"}},
		{"git ", want{words: []string{"git"}, varName: "-"}},
		{"git  checkout   ", want{words: []string{"git", "checkout"}, varName: "-"}},
		{"ls 'my fi", want{words: []string{"ls"}, tok: "my fi", raw: "'my fi", quote: '\'', varName: "-"}},
		{`ls "my fi`, want{words: []string{"ls"}, tok: "my fi", raw: `"my fi`, quote: '"', varName: "-"}},
		{`ls my\ fi`, want{words: []string{"ls"}, tok: "my fi", raw: `my\ fi`, varName: "-"}},
		{`ls my\`, want{words: []string{"ls"}, tok: "my", raw: `my\`, escape: true, varName: "-"}},
		{`ls "a"'b'c`, want{words: []string{"ls"}, tok: "abc", raw: `"a"'b'c`, varName: "-"}},
		{`ls it\'s`, want{words: []string{"ls"}, tok: "it's", raw: `it\'s`, varName: "-"}},
		{`echo "a b" c`, want{words: []string{"echo", "a b"}, tok: "c", raw: "c", varName: "-"}},
		{`echo "a\"b`, want{words: []string{"echo"}, tok: `a"b`, raw: `"a\"b`, quote: '"', varName: "-"}},
		{`echo $'a\tb`, want{words: []string{"echo"}, tok: "a\tb", raw: `$'a\tb`, quote: '$', varName: "-"}},
		{"echo $PA", want{words: []string{"echo"}, tok: "$PA", raw: "$PA", varName: "PA"}},
		{"echo $", want{words: []string{"echo"}, tok: "$", raw: "$", varName: ""}},
		{`echo "$HO`, want{words: []string{"echo"}, tok: "$HO", raw: `"$HO`, quote: '"', varName: "HO"}},
		{"echo ${HO", want{words: []string{"echo"}, tok: "${HO", raw: "${HO", varName: "HO", brace: true}},
		{"echo a$HO", want{words: []string{"echo"}, tok: "a$HO", raw: "a$HO", varName: "HO"}},
		{"echo $HOME/x", want{words: []string{"echo"}, tok: "$HOME/x", raw: "$HOME/x", varName: "-"}},
		{"echo foo; gi", want{tok: "gi", raw: "gi", cmd: -1, varName: "-"}},
		{"echo a | gr", want{tok: "gr", raw: "gr", cmd: -1, varName: "-"}},
		{"echo a && gr", want{tok: "gr", raw: "gr", cmd: -1, varName: "-"}},
		{"echo a\nls -l fo", want{words: []string{"ls", "-l"}, tok: "fo", raw: "fo", varName: "-"}},
		{"ls \\\n-l fo", want{words: []string{"ls", "-l"}, tok: "fo", raw: "fo", varName: "-"}},
		{"echo (gi", want{tok: "gi", raw: "gi", cmd: -1, varName: "-"}},
		{"echo $(git che", want{words: []string{"git"}, tok: "che", raw: "che", varName: "-"}},
		{"echo (git) fo", want{words: []string{"echo", "(git)"}, tok: "fo", raw: "fo", varName: "-"}},
		{"echo `gi", want{tok: "gi", raw: "gi", cmd: -1, varName: "-"}},
		{`echo "$(git ch`, want{words: []string{"git"}, tok: "ch", raw: "ch", varName: "-"}},
		{"diff <(gre", want{tok: "gre", raw: "gre", cmd: -1, varName: "-"}},
		{"cat < fi", want{words: []string{"cat"}, tok: "fi", raw: "fi", redirect: true, varName: "-"}},
		{"cat >fi", want{words: []string{"cat"}, tok: "fi", raw: "fi", redirect: true, varName: "-"}},
		{"cat 2>>fi", want{words: []string{"cat"}, tok: "fi", raw: "fi", redirect: true, varName: "-"}},
		{"cat &>fi", want{words: []string{"cat"}, tok: "fi", raw: "fi", redirect: true, varName: "-"}},
		{"cmd 2>&", want{words: []string{"cmd"}, redirect: true, skip: true, varName: "-"}},
		{"cmd 2>&1 fo", want{words: []string{"cmd"}, tok: "fo", raw: "fo", varName: "-"}},
		{"cmd <<EO", want{words: []string{"cmd"}, tok: "EO", raw: "EO", skip: true, varName: "-"}},
		{"ls # comm", want{skip: true, cmd: -1, varName: "-", raw: "# comm"}},
		{"ls a#b", want{words: []string{"ls"}, tok: "a#b", raw: "a#b", varName: "-"}},
		{"x=$((1+2", want{tok: "x=", raw: "x=$((1+2", skip: true, cmd: -1, varName: "-"}},
		{"FOO=1 gi", want{words: []string{"FOO=1"}, tok: "gi", raw: "gi", cmd: -1, varName: "-"}},
		{"FOO=1 git ch", want{words: []string{"FOO=1", "git"}, tok: "ch", raw: "ch", cmd: 1, varName: "-"}},
		{"sudo gi", want{words: []string{"sudo"}, tok: "gi", raw: "gi", varName: "-"}},
		{"if gi", want{words: []string{"if"}, tok: "gi", raw: "gi", cmd: -1, varName: "-"}},
		{"not command gi", want{words: []string{"not", "command"}, tok: "gi", raw: "gi", cmd: -1, varName: "-"}},
		{"command -v gi", want{words: []string{"command", "-v"}, tok: "gi", raw: "gi", varName: "-"}},
		{"command -", want{words: []string{"command"}, tok: "-", raw: "-", varName: "-"}},
		{"ls ~/Doc", want{words: []string{"ls"}, tok: "~/Doc", raw: "~/Doc", varName: "-"}},
		{"ls --color=al", want{words: []string{"ls"}, tok: "--color=al", raw: "--color=al", varName: "-"}},
	}
	for _, tt := range tests {
		l := scanCompLine(tt.line, len(tt.line))
		var words []string
		for _, w := range l.words {
			words = append(words, w.value)
		}
		varName := "-"
		if l.varPos >= 0 {
			varName = l.buf[l.varPos:l.cursor]
		}
		got := want{
			words:    words,
			tok:      l.tok.value,
			raw:      l.raw(),
			cmd:      l.commandIndex(),
			quote:    l.quote,
			escape:   l.escape,
			varName:  varName,
			brace:    l.varBrace,
			redirect: l.redirect,
			skip:     l.skip,
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("scan %q:\n got %+v\nwant %+v", tt.line, got, tt.want)
		}
		if l.tok.end != len(tt.line) && !l.skip {
			t.Errorf("scan %q: token ends at %d, want the cursor", tt.line, l.tok.end)
		}
	}
}

func TestScanCompLineCursor(t *testing.T) {
	// only the text before the cursor counts
	line := "git checkout main --force"
	l := scanCompLine(line, strings.Index(line, "main")+2)
	if l.tok.value != "ma" || len(l.words) != 2 {
		t.Fatalf("got token %q, %d words", l.tok.value, len(l.words))
	}
}

func TestCompWordSeparators(t *testing.T) {
	tests := []struct {
		raw              string
		slash, slashVal  int
		slashQ           byte
		sep, sepVal      int
		expFirst, expEnd int
	}{
		{"foo", -1, 0, 0, -1, 0, -1, -1},
		{"a/b/c", 4, 4, 0, -1, 0, -1, -1},
		{"'my dir/fi", 8, 7, '\'', -1, 0, -1, -1},
		{`"x/y" z`, -1, 0, 0, -1, 0, -1, -1}, // the token is z
		{`my\ dir/x`, 8, 7, 0, -1, 0, -1, -1},
		{"--file=a/b", 9, 9, 0, 7, 7, -1, -1},
		{"PATH=/a:/b", 9, 9, 0, 8, 8, -1, -1},
		{"$HOME/Doc", 6, 6, 0, -1, 0, 0, 5},
		{"~/Doc", 2, 2, 0, -1, 0, 0, 1},
		{"*.go", -1, 0, 0, -1, 0, 0, 1},
	}
	for _, tt := range tests {
		l := scanCompLine(tt.raw, len(tt.raw))
		w := l.tok
		rel := func(p int) int {
			if p < 0 {
				return p
			}
			return p - w.start
		}
		got := []int{rel(w.slashPos), w.slashVal, int(w.slashQuote), rel(w.sepPos), w.sepVal, rel(w.expFirst), rel(w.expEnd)}
		if w.slashPos < 0 {
			got[1], got[2] = 0, 0
		}
		if w.sepPos < 0 {
			got[4] = 0
		}
		want := []int{tt.slash, tt.slashVal, int(tt.slashQ), tt.sep, tt.sepVal, tt.expFirst, tt.expEnd}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v, want %v", tt.raw, got, want)
		}
	}
}
