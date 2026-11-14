package diff

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsCatalogRace(t *testing.T) {
	cases := map[error]bool{
		&pgconn.PgError{Code: "XX000", Message: "could not open relation with OID 189623"}: true,
		&pgconn.PgError{Code: "XX000", Message: "cache lookup failed for relation 1"}:        true,
		fmt.Errorf("introspection query 8: %w", &pgconn.PgError{Code: "42P01"}):             true,
		&pgconn.PgError{Code: "XX000", Message: "something else"}:                           false,
		&pgconn.PgError{Code: "42501", Message: "permission denied"}:                        false,
		errors.New("connection refused"):                                                    false,
	}
	for err, want := range cases {
		if got := isCatalogRace(err); got != want {
			t.Errorf("isCatalogRace(%v) = %v, want %v", err, got, want)
		}
	}
}
