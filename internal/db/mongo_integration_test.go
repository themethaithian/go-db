package db_test

// This is the acceptance test for the MongoDB adapter: a real MongoDB 7 in
// Docker, reached through the Driver and Conn ports exactly as the Connection
// Registry will reach it.
//
// It skips — never fails — when Docker is not available, like the Redis and
// tunnel tests beside it, and shares one throwaway container across its tests
// the way those do. The server is started with a root user, so the ordinary
// path and the rejected one are the same server rather than two setups.
//
// The docker, condense, firstLine and splitAddress helpers are the tunnel
// test's, and closedAddress and TestMain are the Redis test's; all are shared
// by being in the same package.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/db"
)

const (
	mongoImage = "mongo:7"

	// The root user's credentials. Everything but the auth-failure test uses
	// them, which is what makes that test a rejection rather than a different
	// server.
	mongoUser     = "root"
	mongoPassword = "mongo-integration-pw"

	// The database every Profile in this file names. A MongoDB Profile must
	// name one — see TestIntegrationMongoRequiresADatabase.
	mongoDatabase = "godb_integration"

	// MongoDB takes appreciably longer to come up than Redis: the entrypoint
	// starts a temporary server, creates the root user, stops it and starts the
	// real one. The wait is for that whole sequence, not for a port.
	mongoReadyTimeout = 180 * time.Second
)

func TestIntegrationMongoDriver(t *testing.T) {
	address := startMongoServer(t)

	t.Run("a document written through Exec is read back through ReadQuery", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		affected, err := conn.Exec(ctx, `db.`+collection+`.insertOne({name: "ada", born: 1815, tags: ["maths", "engines"], note: "<b>&</b>"})`)
		if err != nil {
			t.Fatalf("Exec(insertOne): %v", err)
		}
		if affected != 1 {
			t.Errorf("Exec(insertOne) affected = %d, want 1", affected)
		}

		result, err := conn.ReadQuery(ctx, `db.`+collection+`.find({name: 'ada'})`)
		if err != nil {
			t.Fatalf("ReadQuery(find): %v", err)
		}
		if got := result.Kind(); got != db.ResultDocuments {
			t.Fatalf("Kind() = %q, want %q — MongoDB answers with documents", got, db.ResultDocuments)
		}
		set, ok := result.Documents()
		if !ok {
			t.Fatal("Documents() reported the arm absent on a Result tagged documents")
		}
		if len(set.Documents) != 1 {
			t.Fatalf("documents = %d, want the one that was inserted", len(set.Documents))
		}
		if set.Truncated {
			t.Error("Truncated = true on one document, want the cut marker only where there was a cut")
		}

		// Relaxed extended JSON: the values JSON has syntax for are written
		// plainly, and only the object id keeps its $-wrapper. Canonical would
		// have written {"$numberInt": "1815"} instead.
		document := string(set.Documents[0])
		for _, want := range []string{`"name":"ada"`, `"born":1815`, `"tags":["maths","engines"]`, `"_id":{"$oid":`, `"note":"<b>&</b>"`} {
			if !strings.Contains(document, want) {
				t.Errorf("document = %s, want it to contain %s", document, want)
			}
		}
		if strings.Contains(document, "$numberInt") || strings.Contains(document, "$numberLong") {
			t.Errorf("document = %s, want relaxed extended JSON, which writes a number as a number", document)
		}
		if !json.Valid(set.Documents[0]) {
			t.Errorf("document = %s, want valid JSON — the arm is carried as JSON", document)
		}
	})

	t.Run("findOne answers with one document or none", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		if _, err := conn.Exec(ctx, `db.`+collection+`.insertOne({name: "grace"})`); err != nil {
			t.Fatalf("Exec(insertOne): %v", err)
		}

		found, err := conn.ReadQuery(ctx, `db.`+collection+`.findOne({name: "grace"})`)
		if err != nil {
			t.Fatalf("ReadQuery(findOne): %v", err)
		}
		set, _ := found.Documents()
		if len(set.Documents) != 1 || !strings.Contains(string(set.Documents[0]), `"name":"grace"`) {
			t.Errorf("documents = %s, want the one document that matched", set.Documents)
		}

		missing, err := conn.ReadQuery(ctx, `db.`+collection+`.findOne({name: "nobody"})`)
		if err != nil {
			t.Fatalf("ReadQuery(findOne) on no match: %v", err)
		}
		empty, ok := missing.Documents()
		if !ok {
			t.Fatal("a findOne that matched nothing is not the Documents arm, want every read tagged the one way")
		}
		if len(empty.Documents) != 0 {
			t.Errorf("documents = %s, want none — nothing matched, and that is the answer", empty.Documents)
		}
		if empty.Documents == nil {
			t.Error("documents = nil, want an empty list rather than an absent one")
		}
	})

	t.Run("the projection is the second positional argument", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		if _, err := conn.Exec(ctx, `db.`+collection+`.insertOne({name: "ada", born: 1815})`); err != nil {
			t.Fatalf("Exec(insertOne): %v", err)
		}

		result, err := conn.ReadQuery(ctx, `db.`+collection+`.find({}, {name: 1, _id: 0})`)
		if err != nil {
			t.Fatalf("ReadQuery(find with a projection): %v", err)
		}
		set, _ := result.Documents()
		if len(set.Documents) != 1 || string(set.Documents[0]) != `{"name":"ada"}` {
			t.Errorf("documents = %s, want only the projected field", set.Documents)
		}
	})

	t.Run("both counting reads answer with one count document", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		if _, err := conn.Exec(ctx, `db.`+collection+`.insertMany([{n: 1}, {n: 2}, {n: 3}])`); err != nil {
			t.Fatalf("Exec(insertMany): %v", err)
		}

		for _, test := range []struct {
			statement string
			want      string
		}{
			{`db.` + collection + `.countDocuments()`, `{"count":3}`},
			{`db.` + collection + `.countDocuments({n: {$gte: 2}})`, `{"count":2}`},
			{`db.` + collection + `.estimatedDocumentCount()`, `{"count":3}`},
		} {
			result, err := conn.ReadQuery(ctx, test.statement)
			if err != nil {
				t.Fatalf("ReadQuery(%s): %v", test.statement, err)
			}
			set, ok := result.Documents()
			if !ok {
				t.Fatalf("%s is not the Documents arm, want every read tagged the one way", test.statement)
			}
			if len(set.Documents) != 1 || string(set.Documents[0]) != test.want {
				t.Errorf("%s = %s, want %s — go-db's own rendering of a number as a document", test.statement, set.Documents, test.want)
			}
		}
	})

	t.Run("distinct answers with the server's own values shape", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		if _, err := conn.Exec(ctx, `db.`+collection+`.insertMany([{c: "red"}, {c: "red"}, {c: "blue"}, {c: "green"}])`); err != nil {
			t.Fatalf("Exec(insertMany): %v", err)
		}

		result, err := conn.ReadQuery(ctx, `db.`+collection+`.distinct("c")`)
		if err != nil {
			t.Fatalf("ReadQuery(distinct): %v", err)
		}
		set, _ := result.Documents()
		if len(set.Documents) != 1 || string(set.Documents[0]) != `{"values":["blue","green","red"]}` {
			t.Errorf("distinct = %s, want the values under \"values\", as the distinct command itself replies", set.Documents)
		}

		filtered, err := conn.ReadQuery(ctx, `db.`+collection+`.distinct("c", {c: "red"})`)
		if err != nil {
			t.Fatalf("ReadQuery(distinct with a filter): %v", err)
		}
		set, _ = filtered.Documents()
		if len(set.Documents) != 1 || string(set.Documents[0]) != `{"values":["red"]}` {
			t.Errorf("distinct with a filter = %s, want only the filtered value", set.Documents)
		}
	})

	t.Run("aggregate returns what comes out of the pipeline", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		if _, err := conn.Exec(ctx, `db.`+collection+`.insertMany([{c: "red", n: 1}, {c: "red", n: 2}, {c: "blue", n: 5}])`); err != nil {
			t.Fatalf("Exec(insertMany): %v", err)
		}

		result, err := conn.ReadQuery(ctx, `db.`+collection+`.aggregate([{$group: {_id: "$c", total: {$sum: "$n"}}}, {$sort: {_id: 1}}])`)
		if err != nil {
			t.Fatalf("ReadQuery(aggregate): %v", err)
		}
		set, _ := result.Documents()
		if len(set.Documents) != 2 {
			t.Fatalf("documents = %s, want one per group", set.Documents)
		}
		if string(set.Documents[0]) != `{"_id":"blue","total":5}` || string(set.Documents[1]) != `{"_id":"red","total":3}` {
			t.Errorf("documents = %s, want the two groups the pipeline produces", set.Documents)
		}

		withOptions, err := conn.ReadQuery(ctx, `db.`+collection+`.aggregate([{$match: {c: "red"}}], {allowDiskUse: true, comment: "integration"})`)
		if err != nil {
			t.Fatalf("ReadQuery(aggregate with options): %v", err)
		}
		set, _ = withOptions.Documents()
		if len(set.Documents) != 2 {
			t.Errorf("documents = %s, want the two red ones", set.Documents)
		}
	})

	// This is the layer ADR-0006 leaves the classifier carrying alone, running
	// against a server that would happily have done it. The collection $out
	// would have written staying empty is the assertion that matters: the
	// refusal happened before execution, not after.
	t.Run("ReadQuery refuses an aggregation the server would have written with", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		source := freshMongoCollection(t, ctx, conn)
		destination := freshMongoCollection(t, ctx, conn)

		if _, err := conn.Exec(ctx, `db.`+source+`.insertMany([{n: 1}, {n: 2}])`); err != nil {
			t.Fatalf("Exec(insertMany): %v", err)
		}

		for _, statement := range []string{
			`db.` + source + `.aggregate([{$match: {}}, {$out: "` + destination + `"}])`,
			`db.` + source + `.aggregate([{$match: {}}, {$merge: {into: "` + destination + `"}}])`,
		} {
			if _, err := conn.ReadQuery(ctx, statement); !errors.Is(err, db.ErrWriteAttempt) {
				t.Fatalf("ReadQuery(%s) error = %v, want it to wrap db.ErrWriteAttempt", statement, err)
			}
		}

		result, err := conn.ReadQuery(ctx, `db.`+destination+`.countDocuments()`)
		if err != nil {
			t.Fatalf("ReadQuery(countDocuments): %v", err)
		}
		set, _ := result.Documents()
		if string(set.Documents[0]) != `{"count":0}` {
			t.Errorf("the destination holds %s, want it empty — the refused stages must never have reached the server", set.Documents[0])
		}
	})

	t.Run("a result past the cap is cut and says so", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		var insert strings.Builder
		fmt.Fprintf(&insert, "db.%s.insertMany([", collection)
		for i := 0; i <= db.MaxRows; i++ {
			if i > 0 {
				insert.WriteByte(',')
			}
			fmt.Fprintf(&insert, "{i:%d}", i)
		}
		insert.WriteString("])")

		inserted, err := conn.Exec(ctx, insert.String())
		if err != nil {
			t.Fatalf("Exec(insertMany): %v", err)
		}
		if want := int64(db.MaxRows + 1); inserted != want {
			t.Fatalf("Exec(insertMany) affected = %d, want %d", inserted, want)
		}

		result, err := conn.ReadQuery(ctx, `db.`+collection+`.find({})`)
		if err != nil {
			t.Fatalf("ReadQuery(find): %v", err)
		}
		set, _ := result.Documents()
		if len(set.Documents) != db.MaxRows {
			t.Errorf("documents = %d, want the cap of %d", len(set.Documents), db.MaxRows)
		}
		if !set.Truncated {
			t.Error("Truncated = false, want the cut marked so the documents on screen are not read as the whole answer")
		}

		// The count is not capped: it is one document however many were
		// counted, and the number in it is the whole answer.
		counted, err := conn.ReadQuery(ctx, `db.`+collection+`.countDocuments()`)
		if err != nil {
			t.Fatalf("ReadQuery(countDocuments): %v", err)
		}
		set, _ = counted.Documents()
		if want := fmt.Sprintf(`{"count":%d}`, db.MaxRows+1); string(set.Documents[0]) != want {
			t.Errorf("count = %s, want %s", set.Documents[0], want)
		}
	})

	t.Run("an aggregation past the cap is cut and says so", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		var insert strings.Builder
		fmt.Fprintf(&insert, "db.%s.insertMany([", collection)
		for i := 0; i <= db.MaxRows; i++ {
			if i > 0 {
				insert.WriteByte(',')
			}
			fmt.Fprintf(&insert, "{i:%d}", i)
		}
		insert.WriteString("])")

		if _, err := conn.Exec(ctx, insert.String()); err != nil {
			t.Fatalf("Exec(insertMany): %v", err)
		}

		result, err := conn.ReadQuery(ctx, `db.`+collection+`.aggregate([{$match: {}}])`)
		if err != nil {
			t.Fatalf("ReadQuery(aggregate): %v", err)
		}
		set, _ := result.Documents()
		if len(set.Documents) != db.MaxRows || !set.Truncated {
			t.Errorf("documents = %d, truncated = %v; want the cap of %d and the cut marked", len(set.Documents), set.Truncated, db.MaxRows)
		}
	})

	t.Run("the write verbs report what the server changed", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		collection := freshMongoCollection(t, ctx, conn)

		inserted, err := conn.Exec(ctx, `db.`+collection+`.insertMany([{n: 1}, {n: 2}, {n: 3}])`)
		if err != nil {
			t.Fatalf("Exec(insertMany): %v", err)
		}
		if inserted != 3 {
			t.Errorf("Exec(insertMany) affected = %d, want 3", inserted)
		}

		updated, err := conn.Exec(ctx, `db.`+collection+`.updateMany({n: {$gte: 2}}, {$set: {seen: true}})`)
		if err != nil {
			t.Fatalf("Exec(updateMany): %v", err)
		}
		if updated != 2 {
			t.Errorf("Exec(updateMany) affected = %d, want the 2 it changed", updated)
		}

		// Running it again matches the same two and modifies neither: the
		// server counts modified separately from matched, and modified is the
		// one that answers "what did this change".
		again, err := conn.Exec(ctx, `db.`+collection+`.updateMany({n: {$gte: 2}}, {$set: {seen: true}})`)
		if err != nil {
			t.Fatalf("Exec(updateMany) a second time: %v", err)
		}
		if again != 0 {
			t.Errorf("Exec(updateMany) affected = %d on a no-op update, want 0", again)
		}

		upserted, err := conn.Exec(ctx, `db.`+collection+`.updateOne({n: 99}, {$set: {seen: false}}, {upsert: true})`)
		if err != nil {
			t.Fatalf("Exec(updateOne with upsert): %v", err)
		}
		if upserted != 1 {
			t.Errorf("Exec(updateOne with upsert) affected = %d, want the 1 it created", upserted)
		}

		replaced, err := conn.Exec(ctx, `db.`+collection+`.replaceOne({n: 1}, {n: 1, replaced: true})`)
		if err != nil {
			t.Fatalf("Exec(replaceOne): %v", err)
		}
		if replaced != 1 {
			t.Errorf("Exec(replaceOne) affected = %d, want 1", replaced)
		}

		deletedOne, err := conn.Exec(ctx, `db.`+collection+`.deleteOne({n: 99})`)
		if err != nil {
			t.Fatalf("Exec(deleteOne): %v", err)
		}
		if deletedOne != 1 {
			t.Errorf("Exec(deleteOne) affected = %d, want 1", deletedOne)
		}

		deletedMany, err := conn.Exec(ctx, `db.`+collection+`.deleteMany({n: {$gte: 2}})`)
		if err != nil {
			t.Fatalf("Exec(deleteMany): %v", err)
		}
		if deletedMany != 2 {
			t.Errorf("Exec(deleteMany) affected = %d, want the 2 that were left", deletedMany)
		}

		// drop reports 0 for the reason the port gives DDL: the server does not
		// count what it removed, and a number invented here would be invented.
		dropped, err := conn.Exec(ctx, `db.`+collection+`.drop()`)
		if err != nil {
			t.Fatalf("Exec(drop): %v", err)
		}
		if dropped != 0 {
			t.Errorf("Exec(drop) affected = %d, want 0 — dropping is not a counted change", dropped)
		}
	})

	// The Explorer's introspection, against a real catalogue: the database-level
	// read the Database tree browses a MongoDB Profile with, seeded with
	// collections it must find among whatever else the run has left behind.
	t.Run("getCollectionNames lists the database's collections", func(t *testing.T) {
		conn, ctx := openMongo(t, address)
		first := freshMongoCollection(t, ctx, conn)
		second := freshMongoCollection(t, ctx, conn)

		// A collection exists once something is in it.
		for _, collection := range []string{first, second} {
			if _, err := conn.Exec(ctx, `db.`+collection+`.insertOne({seeded: true})`); err != nil {
				t.Fatalf("Exec(insertOne) into %s: %v", collection, err)
			}
		}

		result, err := conn.ReadQuery(ctx, "db.getCollectionNames()")
		if err != nil {
			t.Fatalf("ReadQuery(getCollectionNames): %v", err)
		}
		if got := result.Kind(); got != db.ResultDocuments {
			t.Fatalf("Kind() = %q, want %q — MongoDB answers with documents", got, db.ResultDocuments)
		}
		set, ok := result.Documents()
		if !ok {
			t.Fatal("Documents() reported the arm absent on a Result tagged documents")
		}
		if len(set.Documents) != 1 {
			t.Fatalf("documents = %d, want the one document the names are rendered as", len(set.Documents))
		}

		var document struct {
			Collections []string `json:"collections"`
		}
		if err := json.Unmarshal(set.Documents[0], &document); err != nil {
			t.Fatalf("reading the rendered document: %v", err)
		}
		for _, want := range []string{first, second} {
			if !slices.Contains(document.Collections, want) {
				t.Errorf("collections = %v, want it to hold %q", document.Collections, want)
			}
		}
		if !slices.IsSorted(document.Collections) {
			t.Errorf("collections = %v, want them sorted so two refreshes agree", document.Collections)
		}
	})

	// The other side of the same coin: the database-level form is one call, and
	// the classifier is the only thing between dropDatabase() and a database
	// that is no longer there.
	t.Run("a database-level mutation never reaches the server", func(t *testing.T) {
		conn, ctx := openMongo(t, address)

		if _, err := conn.ReadQuery(ctx, "db.dropDatabase()"); !errors.Is(err, db.ErrWriteAttempt) {
			t.Fatalf("ReadQuery(dropDatabase) error = %v, want it refused as a write attempt", err)
		}
		if _, err := conn.ReadQuery(ctx, "db.getCollectionNames()"); err != nil {
			t.Fatalf("the database is gone or unreadable after a refused dropDatabase: %v", err)
		}
	})

	t.Run("Ping answers on a live connection", func(t *testing.T) {
		conn, ctx := openMongo(t, address)

		if err := conn.Ping(ctx); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})
}

// TestIntegrationMongoRequiresADatabase pins the failure that must not be a
// silent default, and the one place a MongoDB Profile is stricter than a MySQL
// one. The address is a server that is really there, so the only thing being
// tested is that a Profile naming no database fails before anything is dialled:
// go-db reads MongoDB as db.<collection>.<verb>(...), and without a database
// that db names nothing.
func TestIntegrationMongoRequiresADatabase(t *testing.T) {
	address := startMongoServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, database := range []string{"", "   "} {
		profile := mongoProfile(address, database)

		opened, err := db.NewMongoDriver().Open(ctx, profile, mongoPassword, nil)
		if err == nil {
			opened.Close() //nolint:errcheck // it should never have opened
			t.Fatalf("Open with database %q succeeded, want it refused rather than left unset", database)
		}
		if !strings.Contains(err.Error(), "must name the database") {
			t.Errorf("Open error = %v, want it to say a MongoDB profile must name a database", err)
		}
	}
}

// TestIntegrationMongoAuthFailure and its sibling below are the two failures
// the ports promise to tell apart, because they call for different fixes. They
// are also where the driver's error vocabulary is pinned: a rejected credential
// arrives as a server error carrying MongoDB's own code, and an unreachable
// host arrives as a selection timeout with nothing of the sort in it.
func TestIntegrationMongoAuthFailure(t *testing.T) {
	address := startMongoServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, test := range []struct {
		name     string
		user     string
		password string
	}{
		{"the wrong password", mongoUser, "not-the-password"},
		{"no password at all", mongoUser, ""},
		{"a user the server has never heard of", "nobody", mongoPassword},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := mongoProfile(address, mongoDatabase)
			profile.User = test.user

			opened, err := db.NewMongoDriver().Open(ctx, profile, test.password, nil)
			if err == nil {
				opened.Close() //nolint:errcheck // it should never have opened
				t.Fatal("Open succeeded, want it refused")
			}
			if !errors.Is(err, db.ErrAuthFailed) {
				t.Errorf("Open error = %v, want it to wrap db.ErrAuthFailed", err)
			}
			if errors.Is(err, db.ErrUnreachable) {
				t.Error("Open reported the server unreachable; it answered, and said no")
			}
		})
	}
}

func TestIntegrationMongoUnreachable(t *testing.T) {
	// Docker is not needed to prove this one, but the test lives with its
	// sibling and skips with it, so a run with no Docker reports one story.
	startMongoServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	opened, err := db.NewMongoDriver().Open(ctx, mongoProfile(closedAddress(t), mongoDatabase), mongoPassword, nil)
	if err == nil {
		opened.Close() //nolint:errcheck // it should never have opened
		t.Fatal("Open on a closed port succeeded")
	}
	if !errors.Is(err, db.ErrUnreachable) {
		t.Errorf("Open error = %v, want it to wrap db.ErrUnreachable", err)
	}
	if errors.Is(err, db.ErrAuthFailed) {
		t.Error("Open reported the credentials rejected; nothing was there to reject them")
	}
}

// TestIntegrationMongoHonoursTheDialFunc pins the seam a tunnelled Profile
// depends on: the Driver dials through the DialFunc it was given and nowhere
// else. The Profile names an address that does not resolve on this machine, so
// a driver that fell back to dialling directly could not possibly succeed.
func TestIntegrationMongoHonoursTheDialFunc(t *testing.T) {
	address := startMongoServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var mu sync.Mutex
	var asked []string
	dial := func(ctx context.Context, wanted string) (net.Conn, error) {
		mu.Lock()
		asked = append(asked, wanted)
		mu.Unlock()
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}

	profile := mongoProfile("mongo.invalid:27017", mongoDatabase)
	opened, err := db.NewMongoDriver().Open(ctx, profile, mongoPassword, dial)
	if err != nil {
		t.Fatalf("Open through a DialFunc: %v", err)
	}
	t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown

	if err := opened.Ping(ctx); err != nil {
		t.Errorf("Ping through a DialFunc: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(asked) == 0 {
		t.Fatal("the DialFunc was never called, so the connection went around it")
	}
	for _, wanted := range asked {
		if wanted != "mongo.invalid:27017" {
			t.Errorf("the DialFunc was asked for %q, want the Profile's own address every time", wanted)
		}
	}
}

// mongoProfile is a Profile for the shared server, on the given database.
func mongoProfile(address, database string) db.Profile {
	host, port := splitAddress(address)
	return db.Profile{
		Name:     "mongo-integration",
		Host:     host,
		Port:     port,
		User:     mongoUser,
		Database: database,
		Engine:   db.EngineMongoDB,
	}
}

// openMongo opens one connection to the shared server and closes it when the
// test ends. The context it returns is the one the test's calls run under.
func openMongo(t *testing.T, address string) (db.Conn, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	opened, err := db.NewMongoDriver().Open(ctx, mongoProfile(address, mongoDatabase), mongoPassword, nil)
	if err != nil {
		t.Fatalf("opening %s: %v", address, err)
	}
	t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown
	return opened, ctx
}

// freshMongoCollection names a collection nothing else in this file uses, and
// drops it when the test ends. The container is shared, so a collection per
// case is what keeps one case's documents out of another's answers.
func freshMongoCollection(t *testing.T, ctx context.Context, conn db.Conn) string {
	t.Helper()

	name := fmt.Sprintf("c%d", mongoCollectionSeq.Add(1))
	t.Cleanup(func() {
		// Cleanups run last-registered-first, and this one is registered after
		// openMongo's, so the connection is still open when the drop runs.
		conn.Exec(ctx, "db."+name+".drop()") //nolint:errcheck // teardown
	})
	return name
}

var mongoCollectionSeq atomic.Int64

// The container is started at most once per package run and stopped by
// TestMain, so every test in this file shares it.
var (
	mongoOnce      sync.Once
	mongoAddress   string // host:port the container's MongoDB is reachable on
	mongoSkip      string // why the container is unavailable, if it is
	mongoContainer string // container name, empty when nothing was started
)

// startMongoServer returns the shared server's address, starting the container
// on first use. It skips the calling test when Docker is not available, and
// fails it when Docker is there and the server would not come up: a broken
// container is a real failure, not a reason to skip.
func startMongoServer(t *testing.T) string {
	t.Helper()

	mongoOnce.Do(startMongoContainer)
	if mongoSkip != "" {
		t.Skipf("skipping integration test: %s", mongoSkip)
	}
	if mongoAddress == "" {
		t.Fatalf("the %s container did not start", mongoImage)
	}
	return mongoAddress
}

func startMongoContainer() {
	if out, err := dockerCLI("info", "--format", "{{.ServerVersion}}"); err != nil {
		mongoSkip = fmt.Sprintf("Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
		return
	}

	name := fmt.Sprintf("godb-mongo-driver-%d", time.Now().UnixNano())
	if out, err := dockerCLI("run", "--detach", "--rm",
		"--name", name, "--publish", "127.0.0.1::27017",
		"--env", "MONGO_INITDB_ROOT_USERNAME="+mongoUser,
		"--env", "MONGO_INITDB_ROOT_PASSWORD="+mongoPassword,
		mongoImage,
	); err != nil {
		mongoSkip = fmt.Sprintf("could not start %s (%v: %s)", mongoImage, err, condense(out))
		return
	}
	mongoContainer = name

	mapped, err := dockerCLI("port", name, "27017/tcp")
	if err != nil {
		mongoSkip = fmt.Sprintf("could not read the container's mapped port (%v: %s)", err, condense(mapped))
		stopMongoContainer()
		return
	}
	// `docker port` may report several bindings; the first loopback one wins.
	address := firstLine(mapped)
	if address == "" {
		mongoSkip = fmt.Sprintf("the container published no port (%s)", condense(mapped))
		stopMongoContainer()
		return
	}
	if err := waitForMongo(name); err != nil {
		mongoSkip = fmt.Sprintf("%s never became ready (%v)", mongoImage, err)
		stopMongoContainer()
		return
	}
	mongoAddress = address
}

// waitForMongo waits for the server to answer an authenticated ping.
//
// Accepting a connection is not enough here, and that is the difference from
// the Redis wait beside it. The image's entrypoint starts a temporary server,
// creates the root user against it, stops it and starts the real one — so a
// port that answers may belong to the server that is about to be shut down, and
// a test that raced it would read as an adapter failure. Asking as the root
// user proves both that the server is the final one and that the credentials
// the tests use exist.
func waitForMongo(container string) error {
	deadline := time.Now().Add(mongoReadyTimeout)

	var last error
	for time.Now().Before(deadline) {
		out, err := dockerCLI("exec", container, "mongosh", "--quiet",
			"--username", mongoUser, "--password", mongoPassword,
			"--authenticationDatabase", "admin",
			"--eval", "db.adminCommand({ping: 1}).ok",
		)
		if err == nil && strings.Contains(out, "1") {
			return nil
		}
		last = fmt.Errorf("%v: %s", err, condense(out))
		time.Sleep(500 * time.Millisecond)
	}
	return last
}

func stopMongoContainer() {
	if mongoContainer == "" {
		return
	}
	// --rm on the container removes it once stopped.
	dockerCLI("stop", "--time", "0", mongoContainer) //nolint:errcheck // teardown
	mongoContainer = ""
}
