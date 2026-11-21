package diff

import (
	"strings"
	"testing"
)

func TestAccessMethodPartitionedSetBeforeNewPartitions(t *testing.T) {
	live := accessMethodLive("")
	delete(live.Tables, "app.p1")
	live.Partitions, live.PartitionBounds = nil, nil
	d := accessMethodDesired()
	d.Tables["app.p1"].AccessMethod = ""
	p := Plan(live, d, false)
	set, create := -1, -1
	for i, s := range p.Creates {
		switch {
		case s == `alter table "app"."p" set access method "columnar";`:
			set = i
		case strings.HasPrefix(s, `create table if not exists "app"."p1" partition of`):
			create = i
		}
	}
	if set < 0 || create < set {
		t.Errorf("a partition without accessMethod is created after its parent's access method is set, so it gets that one: %v", p.Creates)
	}
}
