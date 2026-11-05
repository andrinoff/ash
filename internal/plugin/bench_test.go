package plugin

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/plugin/plugintest"
)

// benchManager is a manager with the fake plugin in mode loaded (none
// for mode "").
func benchManager(b *testing.B, mode string) *Manager {
	b.Helper()
	m := New(Options{LogDir: b.TempDir()})
	b.Cleanup(func() { m.Close(100 * time.Millisecond) })
	if mode == "" {
		return m
	}
	path, args := plugintest.Command()
	if err := m.Load(Spec{Name: mode, Path: path, Args: args, Dir: "/", Env: append(os.Environ(), plugintest.Env(mode))}); err != nil {
		b.Fatal(err)
	}
	if err := m.WaitReady(mode, 5*time.Second); err != nil {
		b.Fatal(err)
	}
	return m
}

// What every command and prompt costs when no plugin listens.
func BenchmarkEventsNoListeners(b *testing.B) {
	m := benchManager(b, "")
	ps := []int{0}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.PreExec("make test", "/src")
		m.PostExec("make test", 0, time.Second, "/src")
		m.Prompt("/src", 0, ps, time.Second)
	}
}

// One plugin listens to everything: a command and a prompt are encoded
// and queued (the writer goroutine sends them on).
func BenchmarkEventsOneListener(b *testing.B) {
	m := benchManager(b, "sink")
	ps := []int{0}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.PreExec("make test", "/src")
		m.PostExec("make test", 0, time.Second, "/src")
		m.Prompt("/src", 0, ps, time.Second)
	}
}

// Taking actions when there are none: done before every prompt.
func BenchmarkActionsNone(b *testing.B) {
	m := benchManager(b, "")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.Actions()
	}
}

// A command lookup that misses, as for every external command.
func BenchmarkHasCommandMiss(b *testing.B) {
	m := benchManager(b, "sink")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.HasCommand("ls")
	}
}

// A plugin command: request out, answer back.
func BenchmarkRun(b *testing.B) {
	m := benchManager(b, "sink")
	ctx := context.Background()
	args := []string{"x"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Run(ctx, "greet", args, "/", nil); err != nil {
			b.Fatal(err)
		}
	}
}

// A completion the plugin has to answer.
func BenchmarkCompleteUncached(b *testing.B) {
	m := benchManager(b, "sink")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		line := "fruit a" + strconv.Itoa(i)
		if _, ok := m.Complete("fruit", line, len(line), "/", time.Second); !ok {
			b.Fatal("no answer")
		}
	}
}

// The same completion again (a pager redraw, a second tab).
func BenchmarkCompleteCached(b *testing.B) {
	m := benchManager(b, "sink")
	m.Complete("fruit", "fruit a", 7, "/", time.Second)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := m.Complete("fruit", "fruit a", 7, "/", time.Second); !ok {
			b.Fatal("no answer")
		}
	}
}

// Starting a plugin process: what a `plugin load` in config.ash costs
// (the hello is not waited for).
func BenchmarkLoad(b *testing.B) {
	m := benchManager(b, "")
	for i := 0; i < b.N; i++ {
		if err := m.Load(Spec{Name: "cat", Path: "/bin/cat", Args: []string{"cat"}, Dir: "/"}); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := m.Unload("cat"); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

// Stopping a plugin: stdin closed, SIGTERM, reaped.
func BenchmarkUnload(b *testing.B) {
	m := benchManager(b, "")
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if err := m.Load(Spec{Name: "cat", Path: "/bin/cat", Args: []string{"cat"}, Dir: "/"}); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := m.Unload("cat"); err != nil {
			b.Fatal(err)
		}
	}
}
