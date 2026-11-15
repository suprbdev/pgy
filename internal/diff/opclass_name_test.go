package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestIndexOpclassSchemaQualifiers(t *testing.T) {
	ix := &schema.Index{Name: "t_body", Columns: []string{"body"}, Using: "gin", Opclass: "gin_trgm_ops"}
	for live, differ := range map[string]bool{
		"gin_trgm_ops":        false,
		"public.gin_trgm_ops": false,
		"GIN_TRGM_OPS":        false,
		"ext.gin_trgm_ops":    true,
	} {
		if got := indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: live}}); got != differ {
			t.Errorf("live %s: differ = %v, want %v", live, got, differ)
		}
	}
	ix.Opclass = `"ext".gin_trgm_ops`
	if indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "ext.gin_trgm_ops"}}) {
		t.Error("a quoted qualifier must match")
	}
}
