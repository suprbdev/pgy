package validate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	yamlBlockRe   = regexp.MustCompile("(?s)```yaml\n(.*?)```")
	topLevelKeyRe = regexp.MustCompile(`(?m)^(schema \S|tables:|extensions:|roles:|types:|functions:|views:|sequences:|domains:|publications:|eventTriggers:|defaultPrivileges:|schemas:)`)
)

// TestDocsYAMLExamplesValidate checks every complete YAML example in the
// README and docs (a block starting with a top-level schema key) against
// pgy validate, so documented syntax cannot drift from what pgy accepts.
// Fragments (a single table body, a trigger entry) are skipped, and so are
// dependsOn entries naming objects another example declares.
func TestDocsYAMLExamplesValidate(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	files = append(files, filepath.Join("..", "..", "README.md"))
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range yamlBlockRe.FindAllStringSubmatch(strings.ReplaceAll(string(b), "\r\n", "\n"), -1) {
			block := m[1]
			first := ""
			for _, line := range strings.Split(block, "\n") {
				if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
					first = line
					break
				}
			}
			if !topLevelKeyRe.MatchString(first) {
				continue
			}
			p := filepath.Join(t.TempDir(), "example.yml")
			if err := os.WriteFile(p, []byte(block), 0o644); err != nil {
				t.Fatal(err)
			}
			checked++
			for _, is := range Files([]string{p}) {
				if is.Severity == Error && !strings.Contains(is.Message, "matches nothing declared") {
					t.Errorf("%s example %d: %s\n%s", filepath.Base(f), i+1, is.Message, block)
				}
			}
		}
	}
	if checked < 10 {
		t.Errorf("only %d documentation examples checked", checked)
	}
}
