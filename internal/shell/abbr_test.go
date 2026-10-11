package shell

import "testing"

func TestAbbrRenameAndListing(t *testing.T) {
	// --rename onto an existing name silently replaced it.
	out, errOut, _ := runFish(t, `
abbr -a gco git checkout
abbr -a gc2 git commit
abbr --rename gco gc2; echo $status
abbr --rename nope x; echo $status
abbr
`)
	want := "2\n1\nabbr -a -- gco 'git checkout'\nabbr -a -- gc2 'git commit'\n"
	if out != want || errOut == "" {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
	// The listing has to be valid input: --function before --, and the
	// regex as given rather than the compiled wrapper.
	out, _, _ = runFish(t, `
abbr -a --regex 'g.*' gx git
abbr --add --function myfn foo
abbr
`)
	want = "abbr -a --regex 'g.*' -- gx git\nabbr -a --function myfn -- foo\n"
	if out != want {
		t.Fatalf("listing: got %q, want %q", out, want)
	}
}
