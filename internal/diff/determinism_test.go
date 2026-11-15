package diff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPlanOfExamplesIsDeterministic(t *testing.T) {
	paths := []string{}
	err := filepath.Walk(filepath.Join("..", "..", "examples"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".yml") {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil || len(paths) == 0 {
		t.Fatalf("examples: %v", err)
	}
	var first string
	for i := 0; i < 20; i++ {
		d, err := schema.LoadAndMerge(paths)
		if err != nil {
			t.Fatal(err)
		}
		p := Plan(emptyLive(), d, false)
		sql := RenderPrelude(p) + RenderMain(p) + RenderConcurrent(p)
		if i == 0 {
			first = sql
			if strings.Count(sql, "\n") < 100 {
				t.Fatalf("expected a sizeable plan from the examples, got:\n%s", sql)
			}
			continue
		}
		if sql != first {
			t.Fatalf("plan %d differs from the first", i)
		}
	}
}
