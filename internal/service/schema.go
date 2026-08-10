package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// SchemaStatus is the outcome of a schema introspection call — ListDatabases,
// ListTables or ListColumns. They share it because they share the only two
// ways to fail: no connection to ask, or the database refusing the question.
type SchemaStatus string

const (
	// SchemaOK reports that the introspection query ran; the result carries
	// what it found, which may be empty.
	SchemaOK SchemaStatus = "ok"
	// SchemaNotConnected reports that the Profile has no open connection, so
	// there was nowhere to ask.
	SchemaNotConnected SchemaStatus = "not_connected"
	// SchemaFailed reports that the database refused the introspection query
	// on its own terms. Message carries what it said.
	SchemaFailed SchemaStatus = "failed"
)

// DatabaseList is the result of ListDatabases: every schema on the server the
// Profile's credentials can see, ordered by name. Same shape as the lists
// below — Status is what the UI branches on, Databases is set exactly when
// Status is SchemaOK.
type DatabaseList struct {
	Status    SchemaStatus `json:"status"`
	Message   string       `json:"message"`
	Databases []string     `json:"databases,omitempty"`
}

// OK reports whether the databases were listed.
func (r DatabaseList) OK() bool { return r.Status == SchemaOK }

// TableInfo names one table in a Profile's schema — enough for the Database
// tree to list it and let the human ask for its columns.
type TableInfo struct {
	Name string `json:"name"`
	// RowEstimate is InnoDB's own estimate of the table's row count
	// (information_schema.tables.TABLE_ROWS), not an exact count: it is
	// refreshed on ANALYZE TABLE and by background statistics sampling, and
	// can be noticeably off after bulk changes. Nil when the server reports no
	// estimate at all, which happens for views.
	RowEstimate *int64 `json:"row_estimate,omitempty"`
}

// TableList is the result of ListTables, in the house style: Status is what
// the UI branches on, Message is one line of prose fit to show as-is, and
// Tables is set exactly when Status is SchemaOK.
type TableList struct {
	Status  SchemaStatus `json:"status"`
	Message string       `json:"message"`
	Tables  []TableInfo  `json:"tables,omitempty"`
}

// OK reports whether the tables were listed.
func (r TableList) OK() bool { return r.Status == SchemaOK }

// ColumnInfo describes one column of a table, for the Database tree's
// expanded view of it.
type ColumnInfo struct {
	Name string `json:"name"`
	// DataType is the column's full type as MySQL renders it —
	// information_schema.columns.COLUMN_TYPE — e.g. "varchar(32)" or
	// "decimal(10,2) unsigned", not the bare data type MySQL calls DATA_TYPE.
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
	// Key is MySQL's own key flag: "PRI", "UNI", "MUL", or "" when the column
	// is in no index.
	Key string `json:"key"`
}

// ColumnList is the result of ListColumns, in the same shape as TableList.
type ColumnList struct {
	Status  SchemaStatus `json:"status"`
	Message string       `json:"message"`
	Columns []ColumnInfo `json:"columns,omitempty"`
}

// OK reports whether the columns were listed.
func (r ColumnList) OK() bool { return r.Status == SchemaOK }

// IndexInfo describes one index on a table, for the Database tree's expanded
// view of it. Columns is in the index's own seq_in_index order — the order
// that determines which leftmost-prefix queries the index can serve — not
// alphabetical.
type IndexInfo struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
}

// IndexList is the result of ListIndexes, in the same shape as TableList and
// ColumnList.
type IndexList struct {
	Status  SchemaStatus `json:"status"`
	Message string       `json:"message"`
	Indexes []IndexInfo  `json:"indexes,omitempty"`
}

// OK reports whether the indexes were listed.
func (r IndexList) OK() bool { return r.Status == SchemaOK }

// ListDatabases returns every schema the named Profile's connection can see,
// ordered by name, for the Database tree's databases level.
//
// It takes no name of its own: a Profile whose Database field is blank has a
// NULL DATABASE(), which is exactly the case this exists to answer — there is
// nothing to scope the question to yet. System schemas
// (information_schema, mysql, performance_schema, sys) are included as the
// server reports them; where they belong in a tree is the tree's opinion, not
// this package's. Like every read the facade runs, it goes through
// db.Conn.ReadQuery and so executes inside the read-only transaction that
// backstops the Approval Gate.
func (s *AppService) ListDatabases(ctx context.Context, profileName string) DatabaseList {
	conn, err := s.registry.Conn(profileName)
	if err != nil {
		return DatabaseList{Status: SchemaNotConnected, Message: schemaNotConnectedMessage(profileName)}
	}

	rows, err := conn.ReadQuery(ctx,
		"SELECT schema_name FROM information_schema.schemata ORDER BY schema_name")
	if err != nil {
		return DatabaseList{Status: SchemaFailed, Message: oneLine(err)}
	}

	databases := make([]string, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		databases = append(databases, columnValue(row, 0))
	}
	return DatabaseList{Status: SchemaOK, Message: databaseSummary(len(databases)), Databases: databases}
}

// ListTables returns the tables in one schema of the named Profile, ordered by
// name, for the Database tree.
//
// database names the schema to answer for. Blank means the schema the Profile
// itself connects to — table_schema = DATABASE(), which is what every caller
// asked for before there was a databases level, and what the localhost API and
// the MCP proxy still ask for. A non-blank name is compared as an escaped SQL
// string literal, never spliced in as-is. Like every read the facade runs, it
// goes through db.Conn.ReadQuery and so executes inside the read-only
// transaction that backstops the Approval Gate.
func (s *AppService) ListTables(ctx context.Context, profileName, database string) TableList {
	conn, err := s.registry.Conn(profileName)
	if err != nil {
		return TableList{Status: SchemaNotConnected, Message: schemaNotConnectedMessage(profileName)}
	}

	sql := fmt.Sprintf(
		"SELECT table_name, table_rows FROM information_schema.tables "+
			"WHERE table_schema = %s ORDER BY table_name",
		schemaPredicate(database),
	)
	rows, err := conn.ReadQuery(ctx, sql)
	if err != nil {
		return TableList{Status: SchemaFailed, Message: oneLine(err)}
	}

	tables := make([]TableInfo, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		tables = append(tables, TableInfo{
			Name:        columnValue(row, 0),
			RowEstimate: parseRowEstimate(row, 1),
		})
	}
	return TableList{Status: SchemaOK, Message: tableSummary(len(tables)), Tables: tables}
}

// ListColumns returns the columns of table in one schema of the named Profile,
// ordered as MySQL orders them (ordinal_position), for the Database tree's
// expanded view of a table.
//
// database scopes the question exactly as it does for ListTables: blank is the
// Profile's own schema, a name is that schema. Neither it nor table is ever
// spliced into the query as-is: both are rendered as escaped SQL string
// literals (single quotes doubled) and compared in the WHERE clause, exactly
// as any other query text this package hands to db.Conn.ReadQuery — nothing
// here or downstream interpolates raw input into SQL. A table that does not
// exist, or that belongs to another schema, is not an error: it simply has no
// columns, and ListColumns reports SchemaOK with an empty Columns.
func (s *AppService) ListColumns(ctx context.Context, profileName, database, table string) ColumnList {
	conn, err := s.registry.Conn(profileName)
	if err != nil {
		return ColumnList{Status: SchemaNotConnected, Message: schemaNotConnectedMessage(profileName)}
	}

	sql := fmt.Sprintf(
		"SELECT column_name, column_type, is_nullable, column_key FROM information_schema.columns "+
			"WHERE table_schema = %s AND table_name = %s ORDER BY ordinal_position",
		schemaPredicate(database), sqlStringLiteral(table),
	)
	rows, err := conn.ReadQuery(ctx, sql)
	if err != nil {
		return ColumnList{Status: SchemaFailed, Message: oneLine(err)}
	}

	columns := make([]ColumnInfo, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		columns = append(columns, ColumnInfo{
			Name:     columnValue(row, 0),
			DataType: columnValue(row, 1),
			Nullable: columnValue(row, 2) == "YES",
			Key:      columnValue(row, 3),
		})
	}
	return ColumnList{Status: SchemaOK, Message: columnSummary(len(columns), table), Columns: columns}
}

// ListIndexes returns the indexes on table in one schema of the named Profile,
// ordered by index name with each index's columns in seq_in_index order, for
// the Database tree's expanded view of a table.
//
// information_schema.statistics reports one row per (index, column) pair, so
// this groups rows by index_name as it walks them — safe only because the
// query orders by index_name first, keeping every index's rows contiguous.
// PRIMARY is MySQL's own reserved name for the primary key index, so Primary
// is set by name rather than by inspecting constraints separately; Unique
// reflects non_unique = 0, MySQL's own (inverted) unique flag.
//
// database and table are escaped exactly as ListColumns escapes them: never
// spliced in as-is, always escaped SQL string literals. A table that does not
// exist, or that belongs to another schema, is not an error: it simply has no
// indexes, and ListIndexes reports SchemaOK with an empty Indexes.
func (s *AppService) ListIndexes(ctx context.Context, profileName, database, table string) IndexList {
	conn, err := s.registry.Conn(profileName)
	if err != nil {
		return IndexList{Status: SchemaNotConnected, Message: schemaNotConnectedMessage(profileName)}
	}

	sql := fmt.Sprintf(
		"SELECT index_name, column_name, non_unique FROM information_schema.statistics "+
			"WHERE table_schema = %s AND table_name = %s ORDER BY index_name, seq_in_index",
		schemaPredicate(database), sqlStringLiteral(table),
	)
	rows, err := conn.ReadQuery(ctx, sql)
	if err != nil {
		return IndexList{Status: SchemaFailed, Message: oneLine(err)}
	}

	indexes := make([]IndexInfo, 0)
	positions := make(map[string]int, len(rows.Rows))
	for _, row := range rows.Rows {
		name := columnValue(row, 0)
		i, seen := positions[name]
		if !seen {
			indexes = append(indexes, IndexInfo{
				Name:    name,
				Unique:  columnValue(row, 2) == "0",
				Primary: name == "PRIMARY",
			})
			i = len(indexes) - 1
			positions[name] = i
		}
		indexes[i].Columns = append(indexes[i].Columns, columnValue(row, 1))
	}
	return IndexList{Status: SchemaOK, Message: indexSummary(len(indexes), table), Indexes: indexes}
}

// schemaPredicate renders the right-hand side of `table_schema = …` for a
// database argument: DATABASE() for the blank one, and an escaped literal for
// a named one. It is one function rather than three inline ternaries so the
// backwards-compatible reading of "" — the Profile's own schema, which is what
// the localhost API and the MCP proxy rely on — is stated once and cannot
// drift between the three callers.
func schemaPredicate(database string) string {
	if database == "" {
		return "DATABASE()"
	}
	return sqlStringLiteral(database)
}

// sqlStringLiteral renders s as a single-quoted SQL string literal, doubling
// any single quote it contains — the standard SQL escape, and the one MySQL
// itself uses outside NO_BACKSLASH_ESCAPES-sensitive contexts. table came from
// this package's own ListTables, but nothing downstream trusts that: every
// caller of ReadQuery is required not to interpolate raw text, and this is
// what interpolating safely looks like.
func sqlStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// columnValue reads column i of row as a string, treating both a short row and
// a SQL NULL as empty. information_schema never returns NULL for the columns
// this file reads other than table_rows, but a defensive read costs nothing
// and a panic over a driver surprise costs a crashed tree view.
func columnValue(row []*string, i int) string {
	if i >= len(row) || row[i] == nil {
		return ""
	}
	return *row[i]
}

// parseRowEstimate reads column i of row as InnoDB's row estimate. A SQL NULL
// — which information_schema.tables.TABLE_ROWS reports for a view — and text
// that does not parse as an integer both come back nil, honestly: neither is
// an estimate this package can vouch for.
func parseRowEstimate(row []*string, i int) *int64 {
	if i >= len(row) || row[i] == nil {
		return nil
	}
	n, err := strconv.ParseInt(*row[i], 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func schemaNotConnectedMessage(profileName string) string {
	return fmt.Sprintf("Profile %q is not connected: connect it and run the query again.", profileName)
}

func databaseSummary(n int) string {
	if n == 1 {
		return "1 database."
	}
	return fmt.Sprintf("%d databases.", n)
}

func tableSummary(n int) string {
	if n == 1 {
		return "1 table."
	}
	return fmt.Sprintf("%d tables.", n)
}

func columnSummary(n int, table string) string {
	switch n {
	case 0:
		return fmt.Sprintf("Table %q has no columns, or does not exist.", table)
	case 1:
		return fmt.Sprintf("1 column in %q.", table)
	default:
		return fmt.Sprintf("%d columns in %q.", n, table)
	}
}

func indexSummary(n int, table string) string {
	switch n {
	case 0:
		return fmt.Sprintf("Table %q has no indexes, or does not exist.", table)
	case 1:
		return fmt.Sprintf("1 index on %q.", table)
	default:
		return fmt.Sprintf("%d indexes on %q.", n, table)
	}
}
