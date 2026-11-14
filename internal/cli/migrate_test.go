package cli

import (
    "strings"
    "testing"
)

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



func TestChecksumIgnoresCRLF(t *testing.T) {
    lf := "-- pgy\n-- checksum x\n\ncreate table a (id int);\ncreate table b (id int);\n"
    crlf := strings.ReplaceAll(lf, "\n", "\r\n")
    if checksumBody([]byte(lf)) != checksumBody([]byte(crlf)) {
        t.Error("a migration checked out with CRLF line endings must keep its checksum")
    }
}
