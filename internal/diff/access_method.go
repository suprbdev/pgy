package diff

import "fmt"

// accessMethodClause returns the USING clause that creates a relation with
// table access method am, or "" when am is unset. A partitioned table only
// records an access method from PostgreSQL 17 on, so before that the clause
// is left out and warn says so.
func accessMethodClause(fq, am string, partitioned bool, serverVersion int, warn func(string)) string {
	if am == "" {
		return ""
	}
	if partitioned && serverVersion > 0 && serverVersion < 170000 {
		warn(partitionedAccessMethodMessage(fq, serverVersion))
		return ""
	}
	return " using " + quoteIdent(am)
}

// accessMethodStmts returns ALTER ... SET ACCESS METHOD for an existing table
// or materialized view (kind) whose live access method is not am. It
// rewrites the relation, so it needs unsafe; on a partitioned table, from
// PostgreSQL 17 on, it only sets the access method of partitions created
// later and needs nothing. An unset am is unmanaged.
func accessMethodStmts(kind, fq, am string, lt *LiveTable, serverVersion int, unsafe bool, warn func(string)) []string {
	if am == "" || lt == nil || am == lt.AccessMethod {
		return nil
	}
	if lt.Partitioned && serverVersion > 0 && serverVersion < 170000 {
		warn(partitionedAccessMethodMessage(fq, serverVersion))
		return nil
	}
	if !lt.Partitioned && !unsafe {
		return nil
	}
	return []string{fmt.Sprintf("alter %s %s set access method %s;", kind, pqIdent(fq), quoteIdent(am))}
}

func partitionedAccessMethodMessage(fq string, serverVersion int) string {
	return fmt.Sprintf("%s is partitioned, and PostgreSQL %d keeps no access method for a partitioned table (17 does), so its accessMethod is ignored; declare it on the partitions", fq, serverVersion/10000)
}
