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

func TestIndexOpclassOnSearchPath(t *testing.T) {
	useSearchPath(t, "ext")
	ix := &schema.Index{Name: "t_body", Columns: []string{"body"}, Using: "gin", Opclass: "gin_trgm_ops"}
	if indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "ext.gin_trgm_ops"}}) {
		t.Error("an unqualified opclass must match a live one in a schema on the search_path")
	}
	if !indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "ext.gin_trgm_ops2"}}) {
		t.Error("an opclass on the search_path with another name must differ")
	}
	ix.Opclass = "other.gin_trgm_ops"
	if !indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "ext.gin_trgm_ops"}}) {
		t.Error("a qualified opclass in another schema must differ")
	}
}
