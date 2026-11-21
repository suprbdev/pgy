package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildPartitionAccessMethod(t *testing.T) {
	col := func() map[string]*diff.LiveColumn { return map[string]*diff.LiveColumn{"id": {Type: "integer"}} }
	inherited := func() map[string]*diff.LiveColumn { return map[string]*diff.LiveColumn{"id": {Type: "integer", Inherited: true}} }
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables: map[string]*diff.LiveTable{
			"app.p":      {Columns: col(), Partitioned: true, AccessMethod: "custom"},
			"app.p_same": {Columns: inherited(), IsPartition: true, AccessMethod: "custom"},
			"app.p_heap": {Columns: inherited(), IsPartition: true, AccessMethod: "heap"},
			"app.p_sub":  {Columns: inherited(), IsPartition: true, Partitioned: true},
			"app.p_leaf": {Columns: inherited(), IsPartition: true, AccessMethod: "heap"},
			"app.q":      {Columns: col(), Partitioned: true},
			"app.q1":     {Columns: inherited(), IsPartition: true, AccessMethod: "heap"},
			"app.q2":     {Columns: inherited(), IsPartition: true, AccessMethod: "custom"},
			"app.r":      {Columns: col(), Partitioned: true, AccessMethod: "heap"},
			"app.plain":  {Columns: col(), AccessMethod: "custom"},
		},
		DefaultAccessMethod: "heap",
		Details: &diff.LiveDetails{
			ColumnOrder: map[string][]string{},
			PartitionOf: map[string]string{"app.p_same": "app.p", "app.p_heap": "app.p", "app.p_sub": "app.p", "app.p_leaf": "app.p_sub", "app.q1": "app.q", "app.q2": "app.q"},
			PartitionKeys: map[string]string{"app.p": "RANGE (id)", "app.p_sub": "RANGE (id)", "app.q": "RANGE (id)", "app.r": "RANGE (id)"},
			PartitionBounds: map[string]string{"app.p_same": "DEFAULT", "app.p_heap": "DEFAULT", "app.p_sub": "DEFAULT", "app.p_leaf": "DEFAULT", "app.q1": "DEFAULT", "app.q2": "DEFAULT"},
		},
	}
	for fq := range live.Tables {
		live.Details.ColumnOrder[fq] = []string{"id"}
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	am := map[string]string{}
	table := ""
	for _, line := range strings.Split(got, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "table "); ok {
			table = strings.TrimSuffix(name, ":")
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "accessMethod: "); ok {
			am[table] = v
		}
	}
	for table, want := range map[string]string{
		"p": "custom", "p_same": "", "p_heap": "heap", "p_sub": "", "p_leaf": "heap",
		"q": "", "q1": "", "q2": "custom", "r": "heap", "plain": "custom",
	} {
		if am[table] != want {
			t.Errorf("table %s: accessMethod %q, want %q, in:\n%s", table, am[table], want, got)
		}
	}
}

func TestBuildUnsupportedAccessMethod(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables:  map[string]*diff.LiveTable{},
		Details: &diff.LiveDetails{Unsupported: []diff.UnsupportedObject{{Kind: "access method", Name: "custom"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Warnings, "\n") != "access method custom is not supported by pgy and was left out" {
		t.Errorf("want a warning about the access method, got %v", res.Warnings)
	}
}

func TestBuildNotNullNoInherit(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables: map[string]*diff.LiveTable{"app.t": {Columns: map[string]*diff.LiveColumn{
			"a": {Type: "integer", NotNullName: "t_a_nn", NotNullNoInherit: true},
			"b": {Type: "integer", NotNullName: "t_b_not_null"},
		}}},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"a", "b"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{"a: {type: integer, notNullName: t_a_nn, notNullNoInherit: true}", "b: {type: integer}"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildInheritedColumnOverrides(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables: map[string]*diff.LiveTable{
			"app.base": {Columns: map[string]*diff.LiveColumn{
				"id": {Type: "integer", Nullable: true},
				"g":  {Type: "integer", Nullable: true, Generated: "(id + 1)"},
				"e":  {Type: "text", NotNullName: "base_e_nn", NotNullNotValid: "base_e_nn"},
				"n":  {Type: "text", Nullable: true},
			}},
			"app.kid": {Inherits: []string{"app.base"}, Columns: map[string]*diff.LiveColumn{
				"id": {Type: "integer", Nullable: true, Inherited: true},
				"g":  {Type: "integer", Nullable: true, Generated: "(id + 100)", Inherited: true},
				"e":  {Type: "text", NotNullName: "base_e_nn", Inherited: true},
				"n":  {Type: "text", NotNullName: "kid_n_not_null", NotNullNoInherit: true, Inherited: true},
			}},
			"app.same": {Inherits: []string{"app.base"}, Columns: map[string]*diff.LiveColumn{
				"id": {Type: "integer", Nullable: true, Inherited: true},
				"g":  {Type: "integer", Nullable: true, Generated: "(id + 1)", Inherited: true},
				"e":  {Type: "text", NotNullName: "base_e_nn", NotNullNotValid: "base_e_nn", Inherited: true},
				"n":  {Type: "text", Nullable: true, Inherited: true},
			}},
		},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.base": {"id", "g", "e", "n"}, "app.kid": {"id", "g", "e", "n"}, "app.same": {"id", "g", "e", "n"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		"g: {generated: (id + 100)}",
		"e: {notValid: false}",
		"n: {nullable: false, notNullNoInherit: true}",
		"  table same:\n    inherits: [app.base]\n    columns: {}\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
