package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThemeChooseReplacesPreviousTheme(t *testing.T) {
	dir := t.TempDir()
	themes := filepath.Join(dir, "ash", "themes")
	if err := os.MkdirAll(themes, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(themes, "a.theme"), []byte("fish_color_command ff0000\nfish_color_option 00ff00\n"), 0o644)
	os.WriteFile(filepath.Join(themes, "b.theme"), []byte("fish_color_command 0000ff\n"), 0o644)
	sh := New()
	sh.Init()
	sh.SetVar("XDG_CONFIG_HOME", []string{dir}, setOpts{scope: setGlobal, export: 1})
	io := &IO{In: &Stream{}, Out: &Stream{W: &lockedBuffer{}}, Err: &Stream{W: &lockedBuffer{}}}
	if st := sh.RunString("theme choose a; theme choose b", "test", io); st != 0 {
		t.Fatalf("status %d", st)
	}
	if c := sh.Style("command").FG; c.Kind != 2 || c.B != 0xff || c.R != 0 {
		t.Fatalf("command color not from b: %+v", c)
	}
	// b doesn't set option: it must be the default again, not a's green
	if got, want := sh.Style("option"), DefaultTheme().styles["option"]; got != want {
		t.Fatalf("option kept the old theme's style: %+v, want %+v", got, want)
	}
}
