package cli

import "testing"

func TestChecksumParseAndBody(t *testing.T) {
    sql := "-- pgy 0.1.0\n-- checksum deadbeef\n\ncreate table t(id int);\n"
    if parseChecksumHeader(sql) != "deadbeef" {
        t.Fatalf("header parse failed")
    }
    body := checksumBody([]byte(sql))
    if body == "deadbeef" || body == "" { // should be a sha256 not equal to 'deadbeef'
        t.Fatalf("unexpected checksum %s", body)
    }
}


