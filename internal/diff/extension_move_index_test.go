package diff

import (
	"reflect"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRequalifierRewritesIndexColumnKeys(t *testing.T) {
	d := movedCitextDesired()
	d.Tables["app.t"].Indexes = []*schema.Index{{
		Name:              "t_c",
		Columns:           []string{"(btrim(c)::ext.citext)", "id"},
		Opclasses:         map[string]string{"(btrim(c)::ext.citext)": "ext.citext_pattern_ops"},
		Collations:        map[string]string{"(btrim(c)::ext.citext)": "C"},
		StatisticsTargets: map[string]int{"(btrim(c)::ext.citext)": 50},
		With:              map[string]string{"ext.citext": "kept"},
	}}
	rq := newRequalifier(movedCitextLive(), d)
	if rq == nil {
		t.Fatal("no requalifier for a moving extension")
	}
	ix := rq.database(d).Tables["app.t"].Indexes[0]
	if want := []string{"(btrim(c)::public.citext)", "id"}; !reflect.DeepEqual(ix.Columns, want) {
		t.Errorf("columns = %v, want %v", ix.Columns, want)
	}
	if want := map[string]string{"(btrim(c)::public.citext)": "public.citext_pattern_ops"}; !reflect.DeepEqual(ix.Opclasses, want) {
		t.Errorf("opclasses = %v, want %v", ix.Opclasses, want)
	}
	if want := map[string]string{"(btrim(c)::public.citext)": "C"}; !reflect.DeepEqual(ix.Collations, want) {
		t.Errorf("collations = %v, want %v", ix.Collations, want)
	}
	if want := map[string]int{"(btrim(c)::public.citext)": 50}; !reflect.DeepEqual(ix.StatisticsTargets, want) {
		t.Errorf("statistics targets = %v, want %v", ix.StatisticsTargets, want)
	}
	if want := map[string]string{"ext.citext": "kept"}; !reflect.DeepEqual(ix.With, want) {
		t.Errorf("other maps keep their keys, with = %v", ix.With)
	}
}
