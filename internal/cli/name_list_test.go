package cli

import (
	"strings"
	"testing"
)

func TestNameList(t *testing.T) {
	got := nameList([]string{`"Ref"."Plans",Ref.Codes`, ` app.t `, `"a,b".c`, `"Say ""hi""".t`})
	want := `Ref.Plans|Ref.Codes|app.t|a,b.c|Say "hi".t`
	if strings.Join(got, "|") != want {
		t.Errorf("got %q, want %q", strings.Join(got, "|"), want)
	}
}
