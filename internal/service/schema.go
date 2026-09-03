package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/themethaithian/go-db/internal/db"
)

// This file is the Database tree's introspection, on every Engine.
//
// One rule shapes all of it: introspection is just reads. Every answer below is
// produced by a statement handed to db.Conn.ReadQuery — a SELECT over
// information_schema, a SCAN, a db.getCollectionNames() — which means every one
// of them is classified by the Approval Gate, re-checked at the adapter for the
// Engines that have no read-only transaction, and visible in the audit log. No
// Engine gets a private port to answer "what is in here" through, because a
// private port is a read nobody is watching.
//
// What the Engine decides is which read that is, and what the tree's two levels
// mean:
//
//   - MySQL — the databases level is the server's schemas, the tables level is
//     a schema's tables, and a table has columns and indexes under it.
//   - Redis — the databases level is the one index the Profile is connected on,
//     and the tables level is that index's keys, paged out of SCAN.
//   - MongoDB — the databases level is the one database the Profile names, and
//     the tables level is its collections.
//
// The reads are on expand and on refresh, and nothing polls them: the tree
// fetches a level the first time it is opened and not again until a human asks
// (see app/frontend/src/lib/schema.svelte.ts). That is what makes the audit
// entries a Redis or MongoDB Profile now produces a record of what was browsed
// rather than noise.

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
	// SchemaFailed reports that the introspection did not happen: the database
	// refused the query on its own terms, its answer was not one this package
	// could read, or the Profile's Engine has no such question to be asked —
	// a Redis key has no columns. Message carries which, in one line.
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

// TableInfo names one thing the Database tree lists under a database: a MySQL
// table, a Redis key, or a MongoDB collection. It is one type across the three
// because the tree does one thing with all of them — show the name, and browse
// what is inside when it is clicked — and which of the three a Name is, is the
// Profile's Engine rather than a field here.
type TableInfo struct {
	Name string `json:"name"`
	// RowEstimate is InnoDB's own estimate of the table's row count
	// (information_schema.tables.TABLE_ROWS), not an exact count: it is
	// refreshed on ANALYZE TABLE and by background statistics sampling, and
	// can be noticeably off after bulk changes. Nil when the server reports no
	// estimate at all, which happens for views — and always nil off MySQL,
	// where there is nothing to estimate: a Redis key is one key and a MongoDB
	// collection's count is a read of its own.
	RowEstimate *int64 `json:"row_estimate,omitempty"`
}

// TableList is the result of ListTables, in the house style: Status is what
// the UI branches on, Message is one line of prose fit to show as-is, and
// Tables is set exactly when Status is SchemaOK.
type TableList struct {
	Status  SchemaStatus `json:"status"`
	Message string       `json:"message"`
	Tables  []TableInfo  `json:"tables,omitempty"`
	// Truncated reports that Tables is not everything there was: go-db stopped
	// listing before the Engine had run out. It carries ResultSet.Truncated's
	// meaning exactly, one level up — the list on screen is not the whole list,
	// and it says so rather than letting a short tree be read as a small
	// database.
	//
	// Only a Redis keyspace reaches the cap today. It is omitempty so a MySQL
	// answer's JSON is byte for byte what it was before this field existed:
	// information_schema returns a schema's tables whole, and a field that is
	// always false there is one the tree should never have to read.
	Truncated bool `json:"truncated,omitempty"`
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

// ListDatabases returns the databases the named Profile can browse, ordered by
// name, for the Database tree's databases level.
//
// On MySQL that is every schema the connection can see. It takes no name of its
// own: a Profile whose Database field is blank has a NULL DATABASE(), which is
// exactly the case this exists to answer — there is nothing to scope the
// question to yet. System schemas (information_schema, mysql,
// performance_schema, sys) are included as the server reports them; where they
// belong in a tree is the tree's opinion, not this package's. Like every read
// the facade runs, it goes through db.Conn.ReadQuery and so executes inside the
// read-only transaction that backstops the Approval Gate.
//
// On Redis and MongoDB there is exactly one, and it is the Profile's own — see
// redisIndexes and mongoDatabases for why that is the honest answer rather
// than a stub.
func (s *AppService) ListDatabases(ctx context.Context, profileName string) DatabaseList {
	target, status, message := s.schemaTarget(ctx, profileName)
	if status != SchemaOK {
		return DatabaseList{Status: status, Message: message}
	}

	switch target.profile.Engine {
	case db.EngineRedis:
		return redisIndexes(target.profile)
	case db.EngineMongoDB:
		return mongoDatabases(target.profile)
	case db.EngineMySQL:
	default:
		return DatabaseList{Status: SchemaFailed, Message: cannotIntrospect(target.profile.Engine, "databases")}
	}

	rows, err := readTable(ctx, target.conn,
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

// redisIndexes is the Redis databases level: the one index this Profile's
// connection is on, which is its Database field, or 0 for a Profile that names
// none — the index a client that never selects one is already on, and exactly
// what the adapter opened.
//
// It asks the server nothing, and no other index is offered, and both of those
// are decisions rather than gaps:
//
//   - Which index a connection is on is a fact about the Profile, not something
//     to go and ask. The command that would move it, SELECT, is in the
//     classifier's trap list because the editor shares this connection — so
//     there is no read that could answer "and what is in index 2" on the
//     connection in hand.
//
//   - Reaching another index would mean a sibling connection, and the
//     Connection Registry would open one: it treats a database string as
//     whatever the adapter makes of it, and the Redis adapter makes an index.
//     So sibling browsing is possible and simply not built here. What stops it
//     being free is that nothing knows which indexes are worth showing — a
//     server has 16 by default and a client that listed all of them would be
//     listing 15 empty trees. Answering that needs CONFIG GET databases or INFO
//     keyspace, which is a second read and a second shape to be right about,
//     and this task is browsing the one index the human connected to.
func redisIndexes(profile db.Profile) DatabaseList {
	index := strings.TrimSpace(profile.Database)
	if index == "" {
		index = "0"
	}
	return DatabaseList{Status: SchemaOK, Message: databaseSummary(1), Databases: []string{index}}
}

// mongoDatabases is the MongoDB databases level: the one database the Profile
// names, which the adapter refuses to open without.
//
// There is no listing of the server's other databases, and there could not be a
// useful one: go-db's MongoDB grammar pins the leading db to the Profile's
// database, so a collection in another one is not something any statement here
// could read. Showing databases nothing can be run against would be a tree of
// dead ends.
func mongoDatabases(profile db.Profile) DatabaseList {
	database := strings.TrimSpace(profile.Database)
	if database == "" {
		// The adapter refuses to open such a Profile at all, so this is
		// unreachable through a connected one — and is answered honestly rather
		// than with an empty list that would read as "this server has none".
		return DatabaseList{Status: SchemaFailed, Message: "This MongoDB Profile names no database, so there is nothing for go-db to browse: set one on the Profile."}
	}
	return DatabaseList{Status: SchemaOK, Message: databaseSummary(1), Databases: []string{database}}
}

// ListTables returns what the named Profile has under one database, ordered by
// name, for the Database tree: a MySQL schema's tables, a Redis index's keys,
// or a MongoDB database's collections.
//
// On MySQL, database names the schema to answer for. Blank means the schema the
// Profile itself connects to — table_schema = DATABASE(), which is what every
// caller asked for before there was a databases level, and what the localhost
// API and the MCP proxy still ask for. A non-blank name is compared as an
// escaped SQL string literal, never spliced in as-is. Like every read the
// facade runs, it goes through db.Conn.ReadQuery and so executes inside the
// read-only transaction that backstops the Approval Gate.
//
// On Redis and MongoDB, database is ignored, because those Engines have exactly
// one and the connection is already on it (see ListDatabases). The tree passes
// the name it was given back, and the honest thing to do with it is nothing:
// there is no second index or second database this call could be pointed at
// without opening a connection, which introspection does not get to do.
func (s *AppService) ListTables(ctx context.Context, profileName, database string) TableList {
	target, status, message := s.schemaTarget(ctx, profileName)
	if status != SchemaOK {
		return TableList{Status: status, Message: message}
	}

	switch target.profile.Engine {
	case db.EngineRedis:
		// The listing walks everything: no pattern, and the two bounds that
		// keep an expand from pulling a keyspace into a tool window. Which of
		// them cut the list short is not this level's business — the tree shows
		// one Truncated flag — so the cut is dropped here and read by FindKeys,
		// which is answering a question rather than drawing a branch.
		keys, _ := scanRedisKeys(ctx, target.conn, redisScan{count: scanCount, maxPages: maxScanPages})
		return keys
	case db.EngineMongoDB:
		return listMongoCollections(ctx, target.conn)
	case db.EngineMySQL:
	default:
		return TableList{Status: SchemaFailed, Message: cannotIntrospect(target.profile.Engine, "tables")}
	}

	sql := fmt.Sprintf(
		"SELECT table_name, table_rows FROM information_schema.tables "+
			"WHERE table_schema = %s ORDER BY table_name",
		schemaPredicate(database),
	)
	rows, err := readTable(ctx, target.conn, sql)
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
// It is MySQL's alone. A Redis key has no columns and a MongoDB collection has
// no fixed ones, so on those Engines this is refused in those words rather than
// asked and failed — see cannotIntrospect.
func (s *AppService) ListColumns(ctx context.Context, profileName, database, table string) ColumnList {
	target, status, message := s.schemaTarget(ctx, profileName)
	if status != SchemaOK {
		return ColumnList{Status: status, Message: message}
	}
	if target.profile.Engine != db.EngineMySQL {
		return ColumnList{Status: SchemaFailed, Message: cannotIntrospect(target.profile.Engine, "columns")}
	}

	sql := fmt.Sprintf(
		"SELECT column_name, column_type, is_nullable, column_key FROM information_schema.columns "+
			"WHERE table_schema = %s AND table_name = %s ORDER BY ordinal_position",
		schemaPredicate(database), sqlStringLiteral(table),
	)
	rows, err := readTable(ctx, target.conn, sql)
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
// Like ListColumns, it is MySQL's alone: the other two Engines index things
// this call has no shape for, and are refused rather than asked.
func (s *AppService) ListIndexes(ctx context.Context, profileName, database, table string) IndexList {
	target, status, message := s.schemaTarget(ctx, profileName)
	if status != SchemaOK {
		return IndexList{Status: status, Message: message}
	}
	if target.profile.Engine != db.EngineMySQL {
		return IndexList{Status: SchemaFailed, Message: cannotIntrospect(target.profile.Engine, "indexes")}
	}

	sql := fmt.Sprintf(
		"SELECT index_name, column_name, non_unique FROM information_schema.statistics "+
			"WHERE table_schema = %s AND table_name = %s ORDER BY index_name, seq_in_index",
		schemaPredicate(database), sqlStringLiteral(table),
	)
	rows, err := readTable(ctx, target.conn, sql)
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

// FindKeys returns the keys of the named Redis Profile's index whose names
// match text, searched on the server rather than filtered here.
//
// It exists because the Database tree's filter box cannot answer the question
// on its own. The tree lists a keyspace with ListTables, which stops at the
// first thousand keys, and a box that filters that list can only ever narrow
// the thousand: a key beyond them is invisible to a human who knows its exact
// name. Redis's own answer is SCAN's MATCH — the server walks the keyspace and
// returns only what matched — so the thousand becomes a thousand matches
// instead of a thousand keys, which is the difference between a filter and a
// search.
//
// It is Redis's alone, and off Redis it is refused rather than approximated:
// a MySQL schema's tables come back whole, so filtering them where they are
// shown is the honest answer there and there is no server-side search to run.
//
// text is read the way a human means it — see redisMatchPattern — and reaches
// the command line through db.QuoteRedisArgument, never spliced in as-is: the
// same rule every other read in this file follows, with the same reason behind
// it. Only the blank test trims: a key may genuinely have a space at either
// end, so " user" searches for that space.
//
// database is ignored, exactly as ListTables ignores it and for the same
// reason: Redis has one index here and the connection is already on it.
func (s *AppService) FindKeys(ctx context.Context, profileName, database, text string) TableList {
	target, status, message := s.schemaTarget(ctx, profileName)
	if status != SchemaOK {
		return TableList{Status: status, Message: message}
	}
	if target.profile.Engine != db.EngineRedis {
		return TableList{Status: SchemaFailed, Message: cannotSearchKeys(target.profile.Engine)}
	}
	if strings.TrimSpace(text) == "" {
		return TableList{
			Status:  SchemaFailed,
			Message: "There is nothing to search for yet: type some of a key's name, or a pattern like user:*.",
		}
	}

	found, cut := scanRedisKeys(ctx, target.conn, redisScan{
		match: redisMatchPattern(text), count: searchScanCount, maxPages: maxSearchScanPages,
	})
	if found.Status != SchemaOK {
		return found
	}

	// The keys are the walk's; only the sentence is this method's. A search that
	// came back short has something to say that a listing does not — which of
	// the two ways of being short it was.
	found.Message = searchSummary(len(found.Tables), cut)
	return found
}

// redisMatchPattern turns the text a human typed into the glob SCAN's MATCH
// takes. Two readings, and the text itself decides which applies:
//
//   - Text holding a glob character — * ? [ ] — is the pattern, used exactly as
//     it stands. Someone who typed user:* meant a glob, and wrapping it into
//     *user:** would answer a question they did not ask. It is also what
//     RedisInsight does with the same typing, which is where the expectation a
//     human brings to the box comes from.
//
//   - Anything else is a substring: the text wrapped in stars. Typing part of a
//     name and finding the key is what the filter box did when it filtered a
//     list on this side, and a search that demanded a leading star would be a
//     search most people typed wrong.
//
// The one escape is the backslash, and only in the substring reading: a
// backslash is Redis's own escape inside a glob, so an unescaped one would eat
// the character after it and match something else. The other metacharacters
// need no escaping here, because text holding any of them took the first
// branch — it is a pattern, and a pattern's metacharacters are the point of it.
//
// Blank text is the caller's to refuse, and FindKeys refuses it: the substring
// reading would turn it into **, which is every key in the keyspace.
func redisMatchPattern(text string) string {
	if strings.ContainsAny(text, "*?[]") {
		return text
	}
	return "*" + strings.ReplaceAll(text, `\`, `\\`) + "*"
}

// cannotSearchKeys is cannotIntrospect for the question FindKeys asks, and it
// is a refusal for the same reason: an empty list is a real answer a database
// can give, so answering one here would say "nothing matched" where the truth
// is "this is not a thing go-db searches on the server".
func cannotSearchKeys(engine db.Engine) string {
	if !engine.Valid() {
		return fmt.Sprintf("go-db does not know the %q engine this Profile names, so it cannot search it for keys.", engine)
	}
	return fmt.Sprintf(
		"Searching a server for names is Redis's: a %s Profile's tables come back whole, so they are filtered where they are shown.",
		engineName(engine),
	)
}

// The Redis keyspace, and the numbers that bound reading it.
const (
	// maxKeys is how many keys the tree will show for one index. It is MaxRows'
	// argument one shape over — a desktop client renders what a human reads,
	// and a keyspace of millions in a tool window is the unbounded SELECT in
	// different syntax. A list cut here says so.
	maxKeys = 1000

	// scanCount is the COUNT hint each SCAN carries. Redis reads roughly this
	// many slots per call, so one page usually answers a keyspace under the
	// cap, and a big one is reached in a handful of round trips rather than
	// hundreds of tiny ones.
	scanCount = 1000

	// maxScanPages bounds the loop itself, which the cap above does not: SCAN's
	// COUNT is a hint over slots and not a promise of keys, so a large keyspace
	// that has just been emptied can answer page after page with nothing in it
	// while the cursor crawls. Stopping is better than an expand that never
	// finishes, and stopping early is reported as a cut like any other.
	maxScanPages = 32

	// searchScanCount and maxSearchScanPages are the same two numbers for a
	// search rather than a listing, and both are bigger because MATCH changes
	// what a page costs. A SCAN with a pattern still reads about COUNT slots per
	// call, but it returns only the keys that matched — so on the keyspace a
	// human actually searches, where the match is rare, a page of a thousand
	// slots comes back nearly empty and the listing's numbers would spend
	// thirty-two round trips to look at thirty-two thousand slots and find
	// nothing. Ten thousand slots a call is the shape that fits: one round trip
	// per ten thousand, and RedisInsight's own default, so a keyspace searched
	// in both tools takes about the same time in each.
	//
	// A hundred of those pages bounds one search at roughly a million slots.
	// That is the honest trade — long enough to reach a key most people are
	// looking for, short enough that a search over a keyspace far bigger than
	// that comes back rather than hanging, and says it did not finish. The
	// listing's thirty-two would be far too few here, because a search that
	// stops early has failed at its one job in a way a truncated listing has
	// not: the key that was wanted may be exactly the one never reached.
	searchScanCount    = 10000
	maxSearchScanPages = 100
)

// redisScan is the shape of one walk of the keyspace: the pattern MATCH
// carries, the COUNT hint each call gets, and how many calls the walk may make.
//
// It is a struct rather than three arguments because the two callers differ in
// all three at once and in nothing else — the listing walks everything in small
// pages, the search walks for one pattern in large ones — and naming the shape
// keeps that difference in one place a reader can compare rather than spread
// across two call sites.
type redisScan struct {
	// match is the glob to pass to MATCH, or "" for a walk with no pattern,
	// which is every key.
	match string
	// count is the COUNT hint: roughly how many slots Redis reads per call.
	count int
	// maxPages is how many SCAN calls the walk may make before it stops and
	// says so.
	maxPages int
}

// scanCut says how a walk of the keyspace ended, because a list that is not the
// whole list is not one thing. Being cut at the cap and being cut before the
// keyspace was seen mean opposite things to a human: the first says there are
// more of these, narrow it; the second says what you are looking for may still
// be out there. The listing shows both as one Truncated flag; the search says
// which, because it is the answer to a question rather than a browse.
type scanCut int

const (
	// scanWhole: the cursor came home and every key was seen.
	scanWhole scanCut = iota
	// scanCutAtCap: there are more matches than go-db will show.
	scanCutAtCap
	// scanCutUnfinished: the walk gave up with keyspace left unseen — the page
	// bound ran out, or the adapter cut a page of a reply short, which drops
	// keys nobody will ask for again. Either way the keyspace was not fully
	// searched, and that is the one thing this answer has to admit.
	scanCutUnfinished
)

// scanRedisKeys lists the keys of the index conn is open on that scan asks for,
// by paging SCAN through ReadQuery until the cursor comes back to 0 or one of
// the bounds is reached. It returns the list and how the walk ended, so a
// caller can say which of the two short answers it got.
//
// SCAN rather than KEYS, and that is the whole shape of this function. Both are
// on the classifier's allowlist and both write nothing, but KEYS walks the
// entire keyspace in one blocking command, and blocking a server the human is
// working against is a thing a tool window must not do on expand. SCAN's price
// for that is its contract: it pages, it promises no order, and it may return
// the same key on two pages — so the keys are deduplicated as they arrive and
// sorted before they are returned, which is also what makes two refreshes of an
// unchanged keyspace agree.
//
// Anything that is not a SCAN reply is a failure with a line to read. There is
// nothing honest to substitute: an empty keyspace is a real answer, so reporting
// one for a reply nobody could parse would invent it.
func scanRedisKeys(ctx context.Context, conn db.Conn, scan redisScan) (TableList, scanCut) {
	keys := make([]string, 0, maxKeys)
	seen := make(map[string]bool, maxKeys)
	cursor := "0"
	var atCap, unfinished bool

	for page := 0; ; page++ {
		reply, err := readValue(ctx, conn, scan.command(cursor))
		if err != nil {
			return TableList{Status: SchemaFailed, Message: oneLine(err)}, scanWhole
		}

		next, found, cut, err := readScanPage(reply)
		if err != nil {
			return TableList{Status: SchemaFailed, Message: oneLine(err)}, scanWhole
		}
		unfinished = unfinished || cut

		for _, key := range found {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}

		cursor = next
		switch {
		case len(keys) >= maxKeys:
			// The cap was reached. Whether there was more is only known when
			// the cursor came home at the same moment, which is the one case
			// this is not a cut.
			atCap = cursor != "0" || len(keys) > maxKeys
		case cursor == "0":
			// The keyspace was walked whole.
		case page+1 < scan.maxPages:
			continue
		default:
			unfinished = true
		}
		break
	}

	slices.Sort(keys)
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
	}

	// The cap wins when both are true, and it is the more useful of the two to
	// say: a walk that filled the list has more to give whether or not it also
	// ran out of pages, and "there are more" is what the human can act on.
	cut := scanWhole
	switch {
	case atCap:
		cut = scanCutAtCap
	case unfinished:
		cut = scanCutUnfinished
	}

	tables := make([]TableInfo, 0, len(keys))
	for _, key := range keys {
		tables = append(tables, TableInfo{Name: key})
	}
	return TableList{
		Status:    SchemaOK,
		Message:   listSummary(len(tables), "key", "keys", cut != scanWhole),
		Tables:    tables,
		Truncated: cut != scanWhole,
	}, cut
}

// command renders the SCAN this walk sends from cursor. MATCH is left out
// entirely for a walk with no pattern, so the listing's command line is the
// one it always was — and the pattern, when there is one, is quoted by the
// adapter's own quoter rather than pasted in, because it is a human's typing
// and an unquoted space in it would be a second argument.
func (scan redisScan) command(cursor string) string {
	command := "SCAN " + cursor
	if scan.match != "" {
		command += " MATCH " + db.QuoteRedisArgument(scan.match)
	}
	return command + " COUNT " + strconv.Itoa(scan.count)
}

// readScanPage reads one SCAN reply: the cursor to ask next, the keys on this
// page, and whether the adapter cut the page short. A reply that is not that
// shape is an error rather than an empty page, for the reason scanRedisKeys
// gives.
//
// The cursor is checked to be digits before it goes anywhere, and that check is
// load-bearing rather than tidy: the next command is built by putting it into a
// command line. Redis's own cursors are unsigned integers, so nothing is lost —
// and a reply that somehow carried a space would otherwise be a way to append
// arguments to a command the classifier already judged.
func readScanPage(reply db.Reply) (cursor string, keys []string, truncated bool, err error) {
	if reply.Kind != db.ReplyArray || len(reply.Items) != 2 {
		return "", nil, false, errors.New("SCAN answered with something other than a cursor and a list of keys")
	}

	cursor = reply.Items[0].Text
	if reply.Items[0].Kind == db.ReplyInteger {
		cursor = strconv.FormatInt(reply.Items[0].Integer, 10)
	}
	if !isDigits(cursor) {
		return "", nil, false, errors.New("SCAN answered with a cursor that is not a number, and go-db will not put it in the next command")
	}

	page := reply.Items[1]
	if page.Kind != db.ReplyArray {
		return "", nil, false, errors.New("SCAN answered with a page of keys that is not a list")
	}

	keys = make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item.Kind != db.ReplyString {
			return "", nil, false, errors.New("SCAN answered with a key that is not text")
		}
		keys = append(keys, item.Text)
	}
	return cursor, keys, page.Truncated, nil
}

func isDigits(s string) bool {
	if s == "" || len(s) > 20 { // an unsigned 64-bit cursor is 20 digits at most
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// listMongoCollections lists the collections of the database conn is open on,
// through the one database-level read the Approval Gate proved:
// db.getCollectionNames(), which is listCollections with nameOnly.
//
// The adapter renders the names as one document, {"collections": [...]}, the
// way it renders a count as {"count": N} — so this reads that document back
// rather than a bare list. An answer that is not it is a failure: an adapter
// that replied with something else is a bug, and reporting a database with no
// collections would hide it behind a plausible answer.
func listMongoCollections(ctx context.Context, conn db.Conn) TableList {
	docs, err := readDocuments(ctx, conn, "db.getCollectionNames()")
	if err != nil {
		return TableList{Status: SchemaFailed, Message: oneLine(err)}
	}
	if len(docs.Documents) != 1 {
		return TableList{
			Status:  SchemaFailed,
			Message: fmt.Sprintf("Listing the collections came back as %d documents, and go-db reads it as one.", len(docs.Documents)),
		}
	}

	// A pointer, so a document with no collections field at all is told apart
	// from a database with no collections in it. The first is an answer nobody
	// can read; the second is an empty tree.
	var answer struct {
		Collections *[]string `json:"collections"`
	}
	if err := json.Unmarshal(docs.Documents[0], &answer); err != nil {
		return TableList{Status: SchemaFailed, Message: "Listing the collections came back as a document go-db could not read."}
	}
	if answer.Collections == nil {
		return TableList{Status: SchemaFailed, Message: "Listing the collections came back without any collections in it."}
	}

	names := append([]string(nil), *answer.Collections...)
	slices.Sort(names)

	tables := make([]TableInfo, 0, len(names))
	for _, name := range names {
		tables = append(tables, TableInfo{Name: name})
	}
	return TableList{
		Status:    SchemaOK,
		Message:   listSummary(len(tables), "collection", "collections", docs.Truncated),
		Tables:    tables,
		Truncated: docs.Truncated,
	}
}

// schemaTarget resolves what every introspection call needs before it can ask
// anything: the Profile, for the Engine that says which question to ask, and
// the connection to ask it on.
//
// The Engine comes from the saved Profile and never from the caller, for the
// reason engineFor gives about queries — with the difference that a schema call
// runs a statement of this package's own devising, so the Engine decides which
// statement is written rather than only how one is judged.
//
// A Profile that is not saved and a Profile that is not connected are the same
// answer in the same words, because from the tree they are the same mistake:
// there is nowhere to ask.
func (s *AppService) schemaTarget(ctx context.Context, profileName string) (schemaConn, SchemaStatus, string) {
	profile, err := s.profiles.Get(profileName)
	if err != nil {
		if errors.Is(err, db.ErrProfileNotFound) {
			return schemaConn{}, SchemaNotConnected, schemaNotConnectedMessage(profileName)
		}
		return schemaConn{}, SchemaFailed, oneLine(err)
	}

	conn, err := s.registry.Conn(ctx, profileName, "")
	if err != nil {
		return schemaConn{}, SchemaNotConnected, schemaNotConnectedMessage(profileName)
	}
	return schemaConn{profile: profile, conn: conn}, SchemaOK, ""
}

// schemaConn is one Profile and its connection, together because every
// introspection call needs both and neither is useful here without the other.
type schemaConn struct {
	profile db.Profile
	conn    db.Conn
}

// readValue runs one read on conn and unwraps the Value arm of what comes back
// — readTable's sibling for an Engine that answers with one typed value. The
// unwrapping fails loudly for the same reason: a caller asking a Redis question
// and handed a table has been given an answer it cannot read, and an empty
// reply is a real answer Redis can give.
func readValue(ctx context.Context, conn db.Conn, command string) (db.Reply, error) {
	result, err := conn.ReadQuery(ctx, command)
	if err != nil {
		return db.Reply{}, err
	}
	reply, ok := result.Value()
	if !ok {
		return db.Reply{}, fmt.Errorf(
			"this answer came back as %s, which is not the single value this read asks for", resultShape(result))
	}
	return reply, nil
}

// readDocuments is readValue for an Engine that answers with documents.
func readDocuments(ctx context.Context, conn db.Conn, statement string) (db.DocumentSet, error) {
	result, err := conn.ReadQuery(ctx, statement)
	if err != nil {
		return db.DocumentSet{}, err
	}
	docs, ok := result.Documents()
	if !ok {
		return db.DocumentSet{}, fmt.Errorf(
			"this answer came back as %s, which is not the documents this read asks for", resultShape(result))
	}
	return docs, nil
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

// listSummary is tableSummary for the Engines whose tables level holds
// something else, in that thing's own units — and with the one thing a MySQL
// schema listing never has to say: that the list was cut short.
func listSummary(n int, one, many string, truncated bool) string {
	if truncated {
		return fmt.Sprintf("Showing the first %d %s; there are more.", n, many)
	}
	if n == 1 {
		return "1 " + one + "."
	}
	return fmt.Sprintf("%d %s.", n, many)
}

// searchSummary is listSummary for FindKeys, in the units a search counts in
// and with the one distinction a listing never has to draw.
//
// The cap case is listSummary's own sentence, because it is the same sentence:
// there are more of these than are shown. The unfinished case is the one that
// needed writing — "N keys match" would read as the whole answer, and the whole
// answer is exactly what a walk that ran out of pages does not have. It says so
// in the words a human can act on: search again with more of the name.
func searchSummary(n int, cut scanCut) string {
	switch cut {
	case scanCutAtCap:
		return listSummary(n, "matching key", "matching keys", true)
	case scanCutUnfinished:
		return fmt.Sprintf(
			"%d keys found before the search was stopped; the keyspace was not fully searched.", n)
	}
	if n == 1 {
		return "1 key matches."
	}
	return fmt.Sprintf("%d keys match.", n)
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
