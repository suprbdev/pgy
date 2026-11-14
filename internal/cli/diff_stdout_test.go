package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := f()
	os.Stdout = old
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestDiffBufferStdout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "schema app:\n  table t:\n    columns:\n      id: {type: int}\n")
	cfg := &GlobalConfig{SchemaRoot: dir, Schemas: []string{"s.yml"}, BufferPath: "-", Quiet: true}
	run := func(args ...string) (string, error) {
		return captureStdout(t, func() error {
			c := cmdDiff()
			c.SetArgs(args)
			c.SilenceUsage, c.SilenceErrors = true, true
			return c.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, cfg))
		})
	}
	out, err := run("--from-empty")
	var code *exitCodeError
	if !errors.As(err, &code) || code.code != 2 {
		t.Fatalf("expected exit code 2, got %v", err)
	}
	if !strings.Contains(out, `create table if not exists "app"."t"`) {
		t.Errorf("SQL must go to stdout, got %q", out)
	}
	if _, err := os.Stat("-"); err == nil {
		t.Error("no file named - may be written")
	}
	if _, err := run("--from-empty", "--json"); err == nil || !strings.Contains(err.Error(), "cannot be combined with --json") {
		t.Errorf("expected --json conflict, got %v", err)
	}
}
