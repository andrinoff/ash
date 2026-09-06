package shell

import "testing"

func TestCompleteMatchTiers(t *testing.T) {
	tests := []struct {
		needle, s string
		tier      int
	}{
		{"", "anything", tierPrefix},
		{"ma", "main.go", tierPrefix},
		{"MA", "main.go", tierPrefixFold},
		{"ma", "Makefile", tierPrefixFold},
		{"ile", "Makefile", tierSubstr},
		{"ILE", "Makefile", tierSubstrFold},
		{"mfl", "makefile", tierSubseq},
		{"xyz", "makefile", tierNone},
		{"éa", "ÉAU", tierPrefixFold},
	}
	for _, tt := range tests {
		if got := newMatcher(tt.needle, true).tier(tt.s, tierNone); got != tt.tier {
			t.Errorf("%q in %q: tier %d, want %d", tt.needle, tt.s, got, tt.tier)
		}
	}
	if got := newMatcher("ile", false).tier("Makefile", tierNone); got != tierNone {
		t.Errorf("no fuzzy: tier %d", got)
	}
}

func TestCompLess(t *testing.T) {
	for _, l := range [][]string{
		{"-h", "-v", "--all", "--help"},
		{"a", "B", "c"},
		{"B", "b"},
		{"file1", "file2", "file10", "file010a"},
		{"main.go", "Makefile"},
		{"dir/", "Doc/"},
		{"my file", "my folder/", "-dash"},
	} {
		for i := 0; i+1 < len(l); i++ {
			if !compLess(l[i], l[i+1]) || compLess(l[i+1], l[i]) {
				t.Errorf("want %q < %q", l[i], l[i+1])
			}
		}
	}
}

func TestEscapeFor(t *testing.T) {
	tests := []struct {
		s       string
		quote   byte
		atStart bool
		want    string
	}{
		{"plain", 0, true, "plain"},
		{"a b", 0, true, `a\ b`},
		{"$x*?[a]{b}(c)<d>|&;", 0, false, `\$x\*\?\[a\]\{b\}\(c\)\<d\>\|\&\;`},
		{`it's "q" \`, 0, false, `it\'s\ \"q\"\ \\`},
		{"~home", 0, true, `\~home`},
		{"a~b", 0, true, "a~b"},
		{"#x", 0, true, `\#x`},
		{"a#x", 0, true, "a#x"},
		{"tab\there\nnl", 0, false, `tab\there\nnl`},
		{"\x01\xff", 0, false, `\x01\xff`},
		{"ünï", 0, false, "ünï"},
		{"it's", '\'', false, `it'\''s`},
		{`a "b" $c \d`, '"', false, `a \"b\" \$c \\d`},
		{"a'b", '$', false, `a\'b`},
	}
	for _, tt := range tests {
		if got := escapeFor(tt.s, tt.quote, tt.atStart); got != tt.want {
			t.Errorf("escapeFor(%q, %q): %q, want %q", tt.s, tt.quote, got, tt.want)
		}
		// what we produce reads back as the original
		if tt.quote == 0 {
			if got := unescapeToken(escapeFor(tt.s, 0, true)); got != tt.s {
				t.Errorf("round trip %q: %q", tt.s, got)
			}
		}
	}
}
