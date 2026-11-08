package shell

import "testing"

func TestPipestatusAfterSimpleCommand(t *testing.T) {
	// A command that isn't a pipeline used to leave $pipestatus as the
	// last pipeline set it, so after `fortune | cowsay` in a config
	// every prompt showed a failed "0|0" pipeline.
	for src, want := range map[string]string{
		"true | true; false; echo $pipestatus":                     "1\n",
		"true | false; /usr/bin/true; echo $pipestatus":            "0\n",
		"false | true; f() { true | false; }; f; echo $pipestatus": "1\n",
		"true | false; echo ${PIPESTATUS[@]}":                      "0 1\n",
		"true | false && true; echo $pipestatus":                   "0 1\n",
		"true | false || true; echo $pipestatus":                   "0\n",
	} {
		if out, errs, _ := runScript(t, src); out != want {
			t.Errorf("%s: got %q (%q), want %q", src, out, errs, want)
		}
	}
}
