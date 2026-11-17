package cli

import (
    "errors"

    "github.com/jackc/pgx/v5/pgconn"
)

func AsExitCode(err error) (int, bool) {
    var e *exitCodeError
    if errors.As(err, &e) { return e.code, true }
    return 0, false
}

// ErrorDetail returns the DETAIL and HINT of the PostgreSQL error that err
// wraps, each on a line of its own after a leading newline, ready to append
// to the error's message. It is empty when err carries neither.
func ErrorDetail(err error) string {
    var pgErr *pgconn.PgError
    if !errors.As(err, &pgErr) { return "" }
    out := ""
    if pgErr.Detail != "" { out += "\nDETAIL: " + pgErr.Detail }
    if pgErr.Hint != "" { out += "\nHINT: " + pgErr.Hint }
    return out
}
