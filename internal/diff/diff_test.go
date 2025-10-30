package diff

import (
    "testing"

    "github.com/suprbdev/pgy/internal/schema"
)

func TestPlanCreateAndAddColumn(t *testing.T) {
    live := &Live{Tables: map[string]*LiveTable{}}
    desired := &schema.Database{Tables: map[string]*schema.Table{
        "public.users": {Name: "users", Columns: map[string]*schema.Column{
            "id": {Type: "int", Nullable: false},
            "email": {Type: "text", Nullable: false},
        }},
    }}
    p := Plan(live, desired, false)
    if len(p.Creates) != 1 { t.Fatalf("want 1 create, got %d", len(p.Creates)) }
    // now live has table with only id
    live = &Live{Tables: map[string]*LiveTable{"public.users": {Columns: map[string]*LiveColumn{"id": {Type: "int"}}}}}
    p = Plan(live, desired, false)
    if len(p.Alters) != 1 { t.Fatalf("want 1 alter, got %d", len(p.Alters)) }
}


