package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffFromEmptyReportsNoConnectedRoleFindings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "servers:\n  wh: {wrapper: postgres_fdw, options: {dbname: remote}}\nschema app:\n  foreign table orders:\n    server: wh\n    columns:\n      id: {type: int}\n")
	cfg := &GlobalConfig{SchemaRoot: dir, Schemas: []string{"s.yml"}, BufferPath: "-", Quiet: true}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	out, _ := captureStdout(t, func() error {
		c := cmdDiff()
		c.SetArgs([]string{"--from-empty"})
		c.SilenceUsage, c.SilenceErrors = true, true
		return c.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, cfg))
	})
	os.Stderr = old
	_ = w.Close()
	stderr, _ := io.ReadAll(r)
	if !strings.Contains(out, `create server "wh"`) {
		t.Fatalf("expected the server in the plan, got %q", out)
	}
	if strings.Contains(string(stderr), "user mapping") {
		t.Errorf("--from-empty connects to nothing, so it cannot report on user mappings:\n%s", stderr)
	}
}
