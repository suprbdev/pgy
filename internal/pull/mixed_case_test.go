package pull

import (
	"strings"
	"testing"
)

func TestSerialSequenceName(t *testing.T) {
	for seq, want := range map[string]bool{
		"app.orders_id_seq":  true,
		"app.orders_id_seq1": true,
		"app.counter":        false,
		"other.orders_id_seq": false,
	} {
		if got := serialSequenceName("app.orders", "id", seq); got != want {
			t.Errorf("serialSequenceName(%s) = %v, want %v", seq, got, want)
		}
	}
	long := strings.Repeat("t", 40)
	if !serialSequenceName("app."+long, strings.Repeat("c", 30), "app."+long[:30]+"_"+strings.Repeat("c", 28)+"_seq") {
		t.Error("a shortened name must count as the default")
	}
}

func TestPlainIdentsAndPartitionKeys(t *testing.T) {
	if got := strings.Join(plainIdents([]string{`"Qty"`, "qty", `lower("Name")`, `"a""b"`}), "|"); got != `Qty|qty|lower("Name")|a"b` {
		t.Errorf("plainIdents: %s", got)
	}
	n := partitionBy(`RANGE ("At", lower("Name"))`)
	var cols []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "columns" {
			for _, c := range n.Content[i+1].Content {
				cols = append(cols, c.Value)
			}
		}
	}
	if strings.Join(cols, "|") != `At|lower("Name")` {
		t.Errorf("partition columns: %v", cols)
	}
}
