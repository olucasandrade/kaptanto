package backfill

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// KeysetCursor tracks pagination state for a single table snapshot using
// keyset (seek) pagination. It never uses OFFSET — instead it uses a WHERE
// clause on the last seen primary key values (LOG invariant: keyset cursors only).
type KeysetCursor struct {
	// Table is the table name (unqualified).
	Table string
	// Schema is the Postgres schema. If non-empty, queries use schema.table notation.
	Schema string
	// PKCols is the ordered list of primary key column names.
	PKCols []string
	// LastPK holds the values of the last primary key seen. Nil on the first batch.
	LastPK []any
}

// qualifiedTable returns the quoted table reference: "schema"."table" when
// Schema is set, otherwise "table". Identifiers originate from config keys, so
// they are quoted with pgx.Identifier.Sanitize (mirroring publication.go) to
// keep config values from being interpreted as SQL and to support mixed-case
// or reserved-word names.
func (c *KeysetCursor) qualifiedTable() string {
	if c.Schema != "" {
		return pgx.Identifier{c.Schema, c.Table}.Sanitize()
	}
	return pgx.Identifier{c.Table}.Sanitize()
}

// quotedPKCols returns PKCols with each column quoted via pgx.Identifier.
func (c *KeysetCursor) quotedPKCols() []string {
	quoted := make([]string, len(c.PKCols))
	for i, col := range c.PKCols {
		quoted[i] = pgx.Identifier{col}.Sanitize()
	}
	return quoted
}

// BuildFirstQuery returns the SQL and args for the first batch (no WHERE on PK).
// Uses SELECT * so callers receive all columns without knowing the schema.
func (c *KeysetCursor) BuildFirstQuery(batchSize int) (string, []any) {
	orderCols := strings.Join(c.quotedPKCols(), " ASC, ") + " ASC"
	sql := fmt.Sprintf("SELECT * FROM %s ORDER BY %s LIMIT %d",
		c.qualifiedTable(), orderCols, batchSize)
	return sql, nil
}

// BuildNextQuery returns the SQL and args for subsequent batches using keyset pagination.
//
// Single PK:    WHERE pk > $1 ORDER BY pk ASC LIMIT N
// Composite PK: WHERE (pk1, pk2) > ($1, $2) ORDER BY pk1 ASC, pk2 ASC LIMIT N
func (c *KeysetCursor) BuildNextQuery(batchSize int) (string, []any) {
	args := make([]any, len(c.LastPK))
	copy(args, c.LastPK)

	quotedCols := c.quotedPKCols()

	var whereClause string
	if len(quotedCols) == 1 {
		whereClause = fmt.Sprintf("%s > $1", quotedCols[0])
	} else {
		// Composite PK: row-value comparison
		cols := "(" + strings.Join(quotedCols, ", ") + ")"
		placeholders := make([]string, len(quotedCols))
		for i := range quotedCols {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		}
		phStr := "(" + strings.Join(placeholders, ", ") + ")"
		whereClause = fmt.Sprintf("%s > %s", cols, phStr)
	}

	orderParts := make([]string, len(quotedCols))
	for i, col := range quotedCols {
		orderParts[i] = col + " ASC"
	}
	orderClause := strings.Join(orderParts, ", ")

	sql := fmt.Sprintf("SELECT * FROM %s WHERE %s ORDER BY %s LIMIT %d",
		c.qualifiedTable(), whereClause, orderClause, batchSize)

	return sql, args
}
