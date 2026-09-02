package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	// The MongoDB adapter reaches into the Approval Gate for the same reason the
	// Redis one beside it does, and the dependency runs the same one way: guard
	// imports nothing from db. ReadQuery explains why it needs the classifier.
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/mongoql"
)

// MongoDB server error codes this adapter reacts to. Everything else is passed
// through with the server's own message, which is already readable.
//
// The two here are the server saying it reached the credentials and would not
// take them. Unauthorized (13) is deliberately absent: it is the server saying
// the credentials were accepted and the operation was not, which is a different
// fix from a wrong password — the same line the Redis adapter draws around
// NOPERM, and the reason ErrAuthFailed is worth telling apart at all.
//
// Atlas's own 8000 is absent too. It carries a "bad auth" message for a
// rejected password, but 8000 is Atlas's general-purpose code and mapping all
// of it to ErrAuthFailed would report unrelated failures as a wrong password.
const (
	mongoAuthenticationFailed = 18
	mongoUserNotFound         = 11
)

// mongoAuthSource is the database SCRAM credentials are checked against.
//
// It is the driver's own default for SCRAM, set explicitly here so the choice
// is visible rather than inherited. The choice is worth making on purpose,
// because MongoDB's connection strings make a different one: a URI with a
// database in its path authenticates against that database, and falls back to
// admin only when the path is empty.
//
// go-db follows the fallback rather than the path, because a Profile is not a
// URI. Its Database field says which database the statements run in, which is a
// different question from where the account that may run them is defined —
// conflating the two would mean a Profile browsing "orders" could only
// authenticate as an account created inside "orders". Most accounts a desktop
// client is handed live in admin: the root user a server is initialised with,
// and every user Atlas creates.
//
// A Profile whose account lives somewhere else is a case for an authSource of
// its own on the Profile, not for guessing twice at Open. The failure is loud
// and correctly named either way — the wrong source is rejected with
// AuthenticationFailed, which is exactly what it is.
const mongoAuthSource = "admin"

// NewMongoDriver returns the Driver that reaches MongoDB over the official
// mongo-go-driver.
//
// Like the two beside it, it is the only place that knows this server's error
// vocabulary: it turns a rejected credential into ErrAuthFailed and a server
// that was never reached into ErrUnreachable, so callers never import a driver
// package to tell the two apart.
//
// It shares the Redis adapter's one structural difference from MySQL, and the
// difference is a safety property rather than a detail: MongoDB has no
// read-only transaction, so this adapter runs the Approval Gate's MongoDB
// classifier itself before executing a read. See mongoConn.ReadQuery.
func NewMongoDriver() Driver { return mongoDriver{} }

type mongoDriver struct{}

// Open dials profile's server and verifies it answers before returning.
// Nothing is left open on failure.
//
// A MongoDB Profile must name a database, and this is where that is enforced.
// The asymmetry with MySQL is real and deliberate: a MySQL Profile with no
// database is usable, because a SQL statement can qualify its own tables
// (SELECT … FROM other_db.t) and the connection's default schema is only a
// convenience. go-db's MongoDB grammar has no such escape — it accepts
// db.<collection>.<verb>(…) and nothing else, and the leading db is the
// connection's database. Without one there is nowhere for a single statement to
// resolve, so a Profile that does not name a database is refused here rather
// than at every query.
//
// The connection is direct: the driver talks to the host the Profile names and
// discovers nothing else. That is the same shape the Redis adapter has, and for
// a tunnelled Profile it is a requirement rather than a preference — left to
// discover a replica set, the driver would learn its members' own hostnames
// from the server and dial every one of them through the DialFunc, which would
// send them all down one tunnel to one member while looking like a cluster.
//
// The pool settings are the MySQL adapter's, deliberately: they are a statement
// about this application — a desktop client holding few connections per Profile
// so the editor and the Approval Gate do not serialise on one socket — and not
// about any one server.
func (mongoDriver) Open(ctx context.Context, profile Profile, password string, dial DialFunc) (Conn, error) {
	database := strings.TrimSpace(profile.Database)
	if database == "" {
		return nil, fmt.Errorf("db: connection settings for profile %q: %w", profile.Name, errMongoNoDatabase)
	}

	settings := options.Client().
		// The hosts are set as a list rather than assembled into a URI, so the
		// password below is never formatted into a string that could reach a
		// log or an error — the same care the MySQL adapter takes with its
		// connector.
		SetHosts([]string{profile.Address()}).
		SetDirect(true).
		// Bounds both halves of reaching the server, so an unreachable host
		// fails fast instead of hanging the UI: the dial itself, and the wait
		// for a server to become selectable, which is 30 seconds left alone and
		// is what a caller would actually sit through.
		SetConnectTimeout(dialTimeout).
		SetServerSelectionTimeout(dialTimeout).
		SetMaxPoolSize(maxOpenConns).
		SetMaxConnIdleTime(connMaxIdleTime)

	// An empty user is an unauthenticated connection, which is what a server
	// with no access control wants and the only thing it will accept. A server
	// that does require authentication answers its ping either way — MongoDB's
	// ping needs no credentials — and refuses the reads afterwards in its own
	// words, which is the most useful thing that can be said about a Profile
	// that named no user.
	if profile.User != "" {
		settings.SetAuth(options.Credential{
			Username:   profile.User,
			Password:   password,
			AuthSource: mongoAuthSource,
		})
	}

	// Transport encryption, when the Profile asks for one — the same field,
	// meaning the same thing, as it does on the other two Engines.
	//
	// The driver applies it on top of whatever dialler it has: it dials first
	// and upgrades the connection it got back, its own and a custom one alike,
	// so a tunnelled Profile gets TLS to the database at the far end rather
	// than to the tunnel. Nothing has to be wrapped here.
	if secure := profile.tlsConfig(); secure != nil {
		settings.SetTLSConfig(secure)
	}

	if dial != nil {
		// Every connection the driver opens goes this way, including the ones
		// its topology monitor opens later, and there is no path around a
		// dialler it was given: a tunnelled Profile cannot quietly connect
		// direct.
		settings.SetDialer(mongoDialer{dial: dial})
	}

	client, err := mongo.Connect(settings)
	if err != nil {
		return nil, fmt.Errorf("db: connection settings for profile %q: %w", profile.Name, err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		client.Disconnect(context.Background()) //nolint:errcheck // nothing was usable; the close is cleanup
		return nil, mongoError(err)
	}
	return &mongoConn{client: client, database: client.Database(database)}, nil
}

var errMongoNoDatabase = errors.New("db: a MongoDB profile must name the database it works in — go-db reads MongoDB as db.<collection>.<verb>(...), and without one that db names nothing")

// mongoDialer adapts the Driver port's DialFunc to the interface the MongoDB
// driver asks for. The network argument is dropped because the port has no such
// idea: a DialFunc reaches one address, and how it gets there is the Connection
// Registry's business rather than this adapter's.
type mongoDialer struct{ dial DialFunc }

func (d mongoDialer) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	return d.dial(ctx, address)
}

// mongoConn is one Profile's connection pool, pinned to one database. Both are
// private: callers get only what the Conn interface exposes.
type mongoConn struct {
	client   *mongo.Client
	database *mongo.Database
}

// collection names the collection one call works on, inside the database the
// Profile pinned. It is reached from the case that runs the operation rather
// than once before the switch, so that a verb neither method knows is refused
// without the connection being touched at all — which is also what lets both
// refusals be tested without a server.
func (c *mongoConn) collection(call mongoql.Call) *mongo.Collection {
	return c.database.Collection(call.Collection)
}

func (c *mongoConn) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx, readpref.Primary()); err != nil {
		return mongoError(err)
	}
	return nil
}

// ReadQuery runs one statement and returns its answer as the Result union's
// Documents arm — MongoDB answers with documents, and that is the whole of what
// this Engine ever tags them (ADR-0006). Answers that are not documents at all,
// a count and a list of distinct values, are rendered as one; see
// mongoCountDocument and mongoValuesDocument, which say so in the shapes they
// build.
//
// It classifies the statement here, at the connection, and that is the point of
// the method rather than a duplicated check. MySQL's ReadQuery can hand the
// database a statement it only believes is a read because the READ ONLY
// transaction checks the claim too; MongoDB has no such transaction, and
// ADR-0006 accepts that by making the classifier the only layer. So this
// adapter is its own second layer: whatever the Approval Gate decided upstream,
// nothing writes through this method, because the same verdict is taken again
// with the statement that is about to run rather than one that resembles it.
//
// A statement the classifier calls a Mutation comes back wrapping
// ErrWriteAttempt — the same outcome MySQL produces when the database refuses a
// write, and the same reroute: the caller takes it back to the gate rather than
// showing a failure.
//
// Classifying and then parsing looks like reading the statement twice, and it
// is, on purpose. The alternative — parse once and judge the parse — would put
// a second entry point into the classifier and make the two callers' idea of a
// statement drift apart. Both steps are pure functions of the same bytes, so
// running them in this order cannot execute anything the classifier did not
// judge: mongoql.Parse is deterministic, and the call it returns here is the
// call ClassifyMongo saw.
//
// Implementations must not interpolate anything into statement; it is executed
// exactly as given.
func (c *mongoConn) ReadQuery(ctx context.Context, statement string) (Result, error) {
	if verdict := guard.ClassifyMongo(statement); !verdict.IsRead() {
		return Result{}, fmt.Errorf("%w: %s", ErrWriteAttempt, verdict.Reason)
	}

	call, err := mongoql.Parse(statement)
	if err != nil {
		return Result{}, fmt.Errorf("db: %w", err)
	}

	if call.OnDatabase() {
		return c.databaseRead(ctx, call)
	}

	switch call.Verb {
	case "find":
		return mongoFind(ctx, c.collection(call), call.Args)
	case "findOne":
		return mongoFindOne(ctx, c.collection(call), call.Args)
	case "countDocuments":
		return mongoCountDocuments(ctx, c.collection(call), call.Args)
	case "estimatedDocumentCount":
		return mongoEstimatedDocumentCount(ctx, c.collection(call), call.Args)
	case "distinct":
		return mongoDistinct(ctx, c.collection(call), call.Args)
	case "aggregate":
		return mongoAggregate(ctx, c.collection(call), call.Args)
	}

	// Unreachable through the classifier, which allows exactly the operations
	// above — and kept for the day it does not. If the gate's allowlist gains an
	// operation before this adapter learns to run it, the answer is a refusal
	// rather than a verb falling through to nothing.
	return Result{}, fmt.Errorf("db: %s() is classified a read, but go-db does not know how to run it", call.Verb)
}

// databaseRead runs one database-level read — db.<verb>(...), the form that
// names no collection — and is reached only through ReadQuery, after the
// classifier has judged the same statement.
//
// It is a closed set of one for the same reason the collection switch is
// closed: an operation whose shape has not been written down cannot be run
// correctly, and the database-level surface is where the command runners live.
// A verb the gate's own database-level list gains before this method learns to
// run it is refused here rather than falling through to nothing.
func (c *mongoConn) databaseRead(ctx context.Context, call mongoql.Call) (Result, error) {
	if call.Verb == "getCollectionNames" {
		return mongoCollectionNames(ctx, c.database, call.Args)
	}
	return Result{}, fmt.Errorf("db: db.%s() is classified a read, but go-db does not know how to run it", call.Verb)
}

// mongoCollectionNames runs db.getCollectionNames() and answers with the
// database's collection names.
//
// nameOnly is set, and it is the whole of what makes this a catalogue read
// rather than an inspection of the collections themselves: the server is asked
// for names and nothing else — no options, no statistics, no index
// information — which is the operation the Approval Gate proved. The filter is
// the empty document, every collection, because the grammar gives this call no
// arguments to narrow it with.
//
// The answer is the Documents arm holding one document, {"collections": [...]},
// which is go-db's own rendering exactly as a count's {"count": N} is: MongoDB
// replies with a list of names, and the arm carries documents, so the list is
// given the field it is asked by.
func mongoCollectionNames(ctx context.Context, database *mongo.Database, args []mongoql.Value) (Result, error) {
	if len(args) != 0 {
		return Result{}, errors.New("db: getCollectionNames() takes no arguments — it lists every collection in the database go-db is connected to")
	}

	names, err := database.ListCollectionNames(ctx, bson.D{}, options.ListCollections().SetNameOnly(true))
	if err != nil {
		return Result{}, mongoError(err)
	}
	return mongoCollectionNamesDocument(names)
}

// mongoCollectionNamesDocument renders collection names as the one document a
// catalogue read answers with: sorted, and cut at MaxRows.
//
// Sorting is this package's, not the server's. listCollections promises no
// order, so without it the same database would answer differently between two
// refreshes and the cut half would be a different half each time — the same
// reasoning buildRedisMap states for a RESP3 map.
func mongoCollectionNamesDocument(names []string) (Result, error) {
	sorted := append([]string(nil), names...)
	slices.Sort(sorted)

	truncated := len(sorted) > MaxRows
	if truncated {
		sorted = sorted[:MaxRows]
	}

	rendered, err := renderMongoDocument(bson.D{{Key: "collections", Value: sorted}})
	if err != nil {
		return Result{}, err
	}
	return DocumentsResult([]json.RawMessage{rendered}, truncated), nil
}

// Exec runs one approved mutation and reports what MongoDB says it changed.
//
// It does not classify, and the omission is the same one the port describes:
// this is the method the Approval Gate calls once a human has confirmed a
// specific mutation by ID, and refusing it here would refuse the very statement
// that was approved. The safety is upstream, not in this function.
//
// The verbs are a closed set all the same, for a different reason. Every one of
// them has an argument shape this adapter has to be right about — which
// argument is the filter, which is the update, which option keys can be mapped
// without changing what the human asked for — and a verb whose shape has not
// been written down cannot be run correctly. So an operation that is not here
// is refused rather than attempted, and a read that arrives here is refused
// rather than quietly running as a read: Exec is the write path, and a caller
// that reached it with a find has a bug the gate needs told about.
//
// Implementations must not interpolate anything into statement; it is executed
// exactly as given.
func (c *mongoConn) Exec(ctx context.Context, statement string) (int64, error) {
	call, err := mongoql.Parse(statement)
	if err != nil {
		return 0, fmt.Errorf("db: %w", err)
	}

	// Every operation below works on a collection, and a database-level call
	// names none. Refusing here rather than in the switch is what keeps that
	// true: db.insertOne({}) would otherwise reach collection() and be run
	// against a collection with an empty name.
	if call.OnDatabase() {
		return 0, fmt.Errorf("db: db.%s() names no collection, and every MongoDB operation go-db writes with works on one", call.Verb)
	}

	switch call.Verb {
	case "insertOne":
		return mongoInsertOne(ctx, c.collection(call), call.Args)
	case "insertMany":
		return mongoInsertMany(ctx, c.collection(call), call.Args)
	case "updateOne", "updateMany":
		return mongoUpdate(ctx, c.collection(call), call.Verb, call.Args)
	case "replaceOne":
		return mongoReplaceOne(ctx, c.collection(call), call.Args)
	case "deleteOne", "deleteMany":
		return mongoDelete(ctx, c.collection(call), call.Verb, call.Args)
	case "drop":
		return mongoDrop(ctx, c.collection(call), call.Args)
	}

	if mongoReadVerbs[call.Verb] {
		return 0, fmt.Errorf("db: %s() is a read, and reads do not run on the write path", call.Verb)
	}
	return 0, fmt.Errorf("db: go-db does not run %s(): the MongoDB operations it writes with are %s", call.Verb, mongoWriteVerbList)
}

func (c *mongoConn) Close() error {
	if err := c.client.Disconnect(context.Background()); err != nil {
		return fmt.Errorf("db: closing connection: %w", err)
	}
	return nil
}

// mongoReadVerbs names the operations ReadQuery runs. It exists so Exec can
// tell a read that came to the wrong door from an operation nobody has
// implemented, and the two answers are worth telling apart: one is a caller's
// bug and the other is a gap in this adapter.
//
// It is this package's own list rather than a window into the classifier's.
// guard keeps its allowlist unexported, and reaching for it would be asking
// another package to widen its API for a message — so the two are pinned to
// agree by a test instead (see TestMongoReadVerbsAreClassifiedReads).
var mongoReadVerbs = map[string]bool{
	"find":                   true,
	"findOne":                true,
	"countDocuments":         true,
	"estimatedDocumentCount": true,
	"distinct":               true,
	"aggregate":              true,
}

// mongoWriteVerbList is the write set as a human reads it, kept beside the
// switch in Exec that implements it.
const mongoWriteVerbList = "insertOne, insertMany, updateOne, updateMany, replaceOne, deleteOne, deleteMany and drop"

// mongoFind runs find(filter, projection) and returns the matching documents.
//
// The second positional argument is mongosh's projection, and there is no
// third: the cursor helpers a mongosh user would chain after one — .sort(),
// .limit() — are not in go-db's grammar at all, so a call with more arguments
// than this is one whose shape go-db cannot read, and it is refused rather than
// run with the extras dropped.
//
// The cap is set on the server, not just honoured on the way in. A limit of
// MaxRows+1 means the collection is never pulled across the wire to be thrown
// away here, and the one extra document is what makes Truncated mean "there was
// more" rather than "you asked for exactly the cap" — the same peek the Table
// arm's collect does.
func mongoFind(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (Result, error) {
	filter, projection, err := mongoFilterAndProjection("find", args)
	if err != nil {
		return Result{}, err
	}

	settings := options.Find().SetLimit(MaxRows + 1)
	if projection != nil {
		settings.SetProjection(projection)
	}

	cursor, err := collection.Find(ctx, filter, settings)
	if err != nil {
		return Result{}, mongoError(err)
	}
	defer cursor.Close(context.Background()) //nolint:errcheck // the read is done; a failed close changes nothing

	return collectMongoDocuments(ctx, cursor)
}

// mongoFindOne runs findOne(filter, projection). It answers with the Documents
// arm like every other read, holding one document or none — a findOne that
// matched nothing is an empty answer rather than a failure, which is the same
// reading the Redis adapter gives a missing key.
func mongoFindOne(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (Result, error) {
	filter, projection, err := mongoFilterAndProjection("findOne", args)
	if err != nil {
		return Result{}, err
	}

	settings := options.FindOne()
	if projection != nil {
		settings.SetProjection(projection)
	}

	document, err := collection.FindOne(ctx, filter, settings).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return DocumentsResult(nil, false), nil
	}
	if err != nil {
		return Result{}, mongoError(err)
	}

	rendered, err := renderMongoDocument(document)
	if err != nil {
		return Result{}, err
	}
	return DocumentsResult([]json.RawMessage{rendered}, false), nil
}

// mongoCountDocuments runs countDocuments(filter), counting the documents the
// filter matches by actually matching them.
func mongoCountDocuments(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (Result, error) {
	filter, err := mongoOptionalFilter("countDocuments", args)
	if err != nil {
		return Result{}, err
	}

	count, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		return Result{}, mongoError(err)
	}
	return mongoCountDocument(count)
}

// mongoEstimatedDocumentCount runs estimatedDocumentCount(), which reads the
// collection's own metadata and takes no filter — so an argument to it is not a
// narrower question but a misunderstanding, and it is refused rather than
// ignored.
func mongoEstimatedDocumentCount(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (Result, error) {
	if len(args) != 0 {
		return Result{}, errors.New("db: estimatedDocumentCount() takes no arguments — it reads the collection's own count and cannot be filtered; countDocuments(filter) is the one that can")
	}

	count, err := collection.EstimatedDocumentCount(ctx)
	if err != nil {
		return Result{}, mongoError(err)
	}
	return mongoCountDocument(count)
}

// mongoCountDocument renders a count as the one document a counting read
// answers with.
//
// It is go-db's rendering, not a document that exists on the server: MongoDB
// answers a count with a number, and the Documents arm carries documents, so
// the number is given the field name it is asked by. Both counting operations
// use the same field for the same reason a table's column keeps its name — the
// answer to "how many" should look the same however it was counted.
func mongoCountDocument(count int64) (Result, error) {
	rendered, err := renderMongoDocument(bson.D{{Key: "count", Value: count}})
	if err != nil {
		return Result{}, err
	}
	return DocumentsResult([]json.RawMessage{rendered}, false), nil
}

// mongoDistinct runs distinct(field, filter) and answers with the field's
// distinct values.
//
// The values arrive as one document holding them under "values", which is not
// an invention: it is the shape MongoDB's own distinct command replies with,
// and only the driver's helper takes it away by handing back a bare array. So
// the answer a human sees is the answer the server gave, wrapped back up.
//
// The cut, when there is one, is the values list rather than the document list
// — there is only ever one document here — and it is marked on the arm all the
// same, because what Truncated promises is that the answer on screen is not the
// whole answer.
func mongoDistinct(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (Result, error) {
	if len(args) == 0 || len(args) > 2 {
		return Result{}, fmt.Errorf("db: distinct() takes a field name and an optional filter, and this call has %d arguments", len(args))
	}

	field, err := mongoText("distinct", "field name", args[0])
	if err != nil {
		return Result{}, err
	}

	filter := bson.D{}
	if len(args) == 2 {
		if filter, err = mongoDocument("distinct", "filter", args[1]); err != nil {
			return Result{}, err
		}
	}

	var values bson.A
	if err := collection.Distinct(ctx, field, filter).Decode(&values); err != nil {
		return Result{}, mongoError(err)
	}

	truncated := len(values) > MaxRows
	if truncated {
		values = values[:MaxRows]
	}

	rendered, err := renderMongoDocument(bson.D{{Key: "values", Value: values}})
	if err != nil {
		return Result{}, err
	}
	return DocumentsResult([]json.RawMessage{rendered}, truncated), nil
}

// mongoAggregate runs aggregate(pipeline, options) and returns what comes out
// of the pipeline.
//
// The pipeline is sent exactly as it was judged. The cap is applied by reading
// MaxRows+1 documents out of the cursor and closing it, rather than by
// appending a $limit stage — the classifier proved a pipeline, and the pipeline
// that runs has to be that one. Closing early is enough to bound the work:
// documents arrive in batches, so a cursor abandoned after the cap has fetched
// a batch or two rather than the whole aggregation.
func mongoAggregate(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (Result, error) {
	if len(args) == 0 || len(args) > 2 {
		return Result{}, fmt.Errorf("db: aggregate() takes a pipeline and an optional options document, and this call has %d arguments", len(args))
	}
	if args[0].Kind() != mongoql.KindArray {
		return Result{}, errors.New("db: aggregate()'s pipeline must be written as an array of stages, as in [{$match: {}}]")
	}

	var pipeline []bson.D
	if err := bson.UnmarshalExtJSON([]byte(args[0].JSON()), false, &pipeline); err != nil {
		return Result{}, fmt.Errorf("db: aggregate()'s pipeline is not something MongoDB can read: %w", err)
	}

	settings := options.Aggregate()
	if len(args) == 2 {
		if err := applyMongoAggregateOptions(settings, args[1]); err != nil {
			return Result{}, err
		}
	}

	cursor, err := collection.Aggregate(ctx, pipeline, settings)
	if err != nil {
		return Result{}, mongoError(err)
	}
	defer cursor.Close(context.Background()) //nolint:errcheck // the read is done; a failed close changes nothing

	return collectMongoDocuments(ctx, cursor)
}

// applyMongoAggregateOptions maps an aggregate call's options document onto the
// driver's options, and refuses one it cannot map.
//
// The allowlist is short on purpose and it is a safety property rather than
// laziness. An option go-db passed on without understanding could change what
// the operation does; an option go-db dropped silently would run something
// other than what was written and approved. Refusing is the only answer that is
// neither. The four here each mean one thing, and this function can say exactly
// what it sends for each.
//
// The offending key is not named in the refusal. It is the caller's own text,
// and refusals are quoted into the editor badge and the audit log — the same
// discipline internal/mongoql keeps — so the message says what go-db does
// accept instead.
func applyMongoAggregateOptions(settings *options.AggregateOptionsBuilder, document mongoql.Value) error {
	if document.Kind() != mongoql.KindObject {
		return errors.New("db: aggregate()'s options must be written as a document, as in {allowDiskUse: true}")
	}

	for _, field := range document.Fields() {
		switch field.Name {
		case "allowDiskUse":
			allow, err := mongoFlag("aggregate", "allowDiskUse", field.Value)
			if err != nil {
				return err
			}
			settings.SetAllowDiskUse(allow)

		case "comment":
			comment, err := mongoText("aggregate", "comment option", field.Value)
			if err != nil {
				return err
			}
			settings.SetComment(comment)

		case "hint":
			// An index is named either way a MongoDB index can be named: by its
			// name, or by the key document it was built from.
			switch field.Value.Kind() {
			case mongoql.KindString:
				name, err := mongoText("aggregate", "hint option", field.Value)
				if err != nil {
					return err
				}
				settings.SetHint(name)
			case mongoql.KindObject:
				keys, err := mongoDocument("aggregate", "hint option", field.Value)
				if err != nil {
					return err
				}
				settings.SetHint(keys)
			default:
				return errors.New("db: aggregate()'s hint option must be an index name or an index key document")
			}

		case "let":
			variables, err := mongoDocument("aggregate", "let option", field.Value)
			if err != nil {
				return err
			}
			settings.SetLet(variables)

		default:
			return errors.New("db: the aggregate() options go-db sends are allowDiskUse, comment, hint and let, and this call has another one — go-db will not send an option it cannot map, nor drop one that was written")
		}
	}
	return nil
}

// mongoInsertOne runs insertOne(document) and reports the one document it
// inserted. The count is not the server's: MongoDB answers an insert with the
// id it assigned rather than a number, and one insert that did not fail
// inserted one document.
func mongoInsertOne(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (int64, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("db: insertOne() takes one document to insert, and this call has %d arguments", len(args))
	}

	document, err := mongoDocument("insertOne", "document", args[0])
	if err != nil {
		return 0, err
	}
	if _, err := collection.InsertOne(ctx, document); err != nil {
		return 0, mongoError(err)
	}
	return 1, nil
}

// mongoInsertMany runs insertMany([documents]) and reports how many the server
// said it inserted, which is the length of the ids it assigned.
func mongoInsertMany(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (int64, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("db: insertMany() takes one array of documents to insert, and this call has %d arguments", len(args))
	}
	if args[0].Kind() != mongoql.KindArray {
		return 0, errors.New("db: insertMany()'s argument must be written as an array of documents, as in [{}, {}]")
	}

	var documents []bson.D
	if err := bson.UnmarshalExtJSON([]byte(args[0].JSON()), false, &documents); err != nil {
		return 0, fmt.Errorf("db: insertMany()'s documents are not something MongoDB can read: %w", err)
	}
	if len(documents) == 0 {
		return 0, errors.New("db: insertMany() was given no documents to insert")
	}

	// The driver takes a slice of anything; it is given the documents one by
	// one rather than the typed slice, because a typed slice of bson.D is not
	// what its reflection expects to walk.
	each := make([]any, len(documents))
	for i, document := range documents {
		each[i] = document
	}

	result, err := collection.InsertMany(ctx, each)
	if err != nil {
		return 0, mongoError(err)
	}
	return int64(len(result.InsertedIDs)), nil
}

// mongoUpdate runs updateOne(filter, update, options) or updateMany, and
// reports what changed.
//
// The count is the modified documents plus the upserted one. A document that
// matched but was already in the state the update asks for is not counted, and
// that is the server's own arithmetic rather than this adapter's opinion:
// MongoDB reports matched and modified separately, and modified is the one that
// answers "what did this change".
func mongoUpdate(ctx context.Context, collection *mongo.Collection, verb string, args []mongoql.Value) (int64, error) {
	filter, update, upsert, err := mongoWriteArguments(verb, "update", args)
	if err != nil {
		return 0, err
	}

	if verb == "updateOne" {
		result, err := collection.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(upsert))
		if err != nil {
			return 0, mongoError(err)
		}
		return result.ModifiedCount + result.UpsertedCount, nil
	}

	result, err := collection.UpdateMany(ctx, filter, update, options.UpdateMany().SetUpsert(upsert))
	if err != nil {
		return 0, mongoError(err)
	}
	return result.ModifiedCount + result.UpsertedCount, nil
}

// mongoReplaceOne runs replaceOne(filter, replacement, options) and counts what
// mongoUpdate counts, for the same reason.
func mongoReplaceOne(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (int64, error) {
	filter, replacement, upsert, err := mongoWriteArguments("replaceOne", "replacement", args)
	if err != nil {
		return 0, err
	}

	result, err := collection.ReplaceOne(ctx, filter, replacement, options.Replace().SetUpsert(upsert))
	if err != nil {
		return 0, mongoError(err)
	}
	return result.ModifiedCount + result.UpsertedCount, nil
}

// mongoDelete runs deleteOne(filter) or deleteMany(filter) and reports the
// documents the server removed.
func mongoDelete(ctx context.Context, collection *mongo.Collection, verb string, args []mongoql.Value) (int64, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("db: %s() takes one filter, and this call has %d arguments", verb, len(args))
	}

	filter, err := mongoDocument(verb, "filter", args[0])
	if err != nil {
		return 0, err
	}

	if verb == "deleteOne" {
		result, err := collection.DeleteOne(ctx, filter)
		if err != nil {
			return 0, mongoError(err)
		}
		return result.DeletedCount, nil
	}

	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, mongoError(err)
	}
	return result.DeletedCount, nil
}

// mongoDrop runs drop() and reports 0.
//
// Zero is the honest answer rather than a missing one, and it is the precedent
// the port already sets for DDL: "DDL reports 0 too: MySQL counts rows, and
// CREATE TABLE changes none." Dropping a collection removes every document in
// it and reports none of them, because the server does not count them on the
// way out and inventing a number here would be inventing it.
func mongoDrop(ctx context.Context, collection *mongo.Collection, args []mongoql.Value) (int64, error) {
	if len(args) != 0 {
		return 0, fmt.Errorf("db: drop() takes no arguments, and this call has %d", len(args))
	}
	if err := collection.Drop(ctx); err != nil {
		return 0, mongoError(err)
	}
	return 0, nil
}

// Index creation is deliberately not in the set above, and the omission is
// worth writing down because createIndex() is the operation most likely to be
// missed.
//
// Its options are the reason. An index is defined by its keys and by a document
// of options — unique, partialFilterExpression, expireAfterSeconds, collation —
// and every one of them changes what the index is rather than how it is built.
// An adapter that mapped some of them and dropped the rest would build a
// different index from the one a human wrote and a human approved, silently,
// and there is no count coming back to notice it by. Refusing until each option
// has been written down is the same discipline the classifier's allowlists
// keep: adding one later is cheap, and getting one wrong now is an incident.

// mongoFilterAndProjection reads the arguments the two find operations share: a
// filter, and mongosh's positional projection after it. Both are optional, and
// an absent filter is the empty one — find() with nothing in it is every
// document, which is what mongosh means by it too.
func mongoFilterAndProjection(verb string, args []mongoql.Value) (bson.D, bson.D, error) {
	if len(args) > 2 {
		return nil, nil, fmt.Errorf("db: %s() takes a filter and an optional projection, and this call has %d arguments", verb, len(args))
	}

	filter := bson.D{}
	if len(args) >= 1 {
		parsed, err := mongoDocument(verb, "filter", args[0])
		if err != nil {
			return nil, nil, err
		}
		filter = parsed
	}

	var projection bson.D
	if len(args) == 2 {
		parsed, err := mongoDocument(verb, "projection", args[1])
		if err != nil {
			return nil, nil, err
		}
		projection = parsed
	}
	return filter, projection, nil
}

// mongoOptionalFilter reads the single optional filter a counting read takes.
func mongoOptionalFilter(verb string, args []mongoql.Value) (bson.D, error) {
	switch len(args) {
	case 0:
		return bson.D{}, nil
	case 1:
		return mongoDocument(verb, "filter", args[0])
	}
	return nil, fmt.Errorf("db: %s() takes an optional filter, and this call has %d arguments", verb, len(args))
}

// mongoWriteArguments reads the shape the update and replace operations share:
// a filter, the document that changes what it matched, and an options document
// whose only key go-db maps is upsert.
//
// Upsert is the only one because it is the only one that changes what the
// operation does in a way the count reports — everything else on that document
// either does not apply to a desktop client's one-off write or would have to be
// mapped before it could be sent, which is the same line applyMongoAggregateOptions
// draws.
func mongoWriteArguments(verb, second string, args []mongoql.Value) (bson.D, bson.D, bool, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, nil, false, fmt.Errorf("db: %s() takes a filter, a %s and an optional options document, and this call has %d arguments", verb, second, len(args))
	}

	filter, err := mongoDocument(verb, "filter", args[0])
	if err != nil {
		return nil, nil, false, err
	}
	change, err := mongoDocument(verb, second, args[1])
	if err != nil {
		return nil, nil, false, err
	}

	upsert := false
	if len(args) == 3 {
		if args[2].Kind() != mongoql.KindObject {
			return nil, nil, false, fmt.Errorf("db: %s()'s options must be written as a document, as in {upsert: true}", verb)
		}
		for _, field := range args[2].Fields() {
			if field.Name != "upsert" {
				return nil, nil, false, fmt.Errorf("db: the only %s() option go-db sends is upsert, and this call has another one — go-db will not send an option it cannot map, nor drop one that was written", verb)
			}
			if upsert, err = mongoFlag(verb, "upsert", field.Value); err != nil {
				return nil, nil, false, err
			}
		}
	}
	return filter, change, upsert, nil
}

// mongoDocument converts one parsed argument into the document the driver
// sends.
//
// The shape is checked against the parse before the bytes are unmarshalled,
// because the two refusals are not the same refusal: a filter written as an
// array or a bare null has to be told it is not a document, and extended JSON's
// unmarshaller would take null for an empty document without saying anything.
//
// What is unmarshalled is mongoql's own rendering rather than the input's
// bytes, which is what makes this safe to hand to the driver at all: single
// quotes have become double, unquoted keys are quoted, and nothing the parser
// did not understand is in it.
func mongoDocument(verb, what string, value mongoql.Value) (bson.D, error) {
	if value.Kind() != mongoql.KindObject {
		return nil, fmt.Errorf("db: %s()'s %s must be written as a document, as in {}", verb, what)
	}

	var document bson.D
	if err := bson.UnmarshalExtJSON([]byte(value.JSON()), false, &document); err != nil {
		return nil, fmt.Errorf("db: %s()'s %s is not something MongoDB can read: %w", verb, what, err)
	}
	return document, nil
}

// mongoText reads a parsed string argument. It decodes mongoql's rendering
// rather than reaching for the parse's own text, so every value this adapter
// sends has come the one way.
func mongoText(verb, what string, value mongoql.Value) (string, error) {
	if value.Kind() != mongoql.KindString {
		return "", fmt.Errorf("db: %s()'s %s must be written as a string, in quotes", verb, what)
	}

	var text string
	if err := json.Unmarshal([]byte(value.JSON()), &text); err != nil {
		return "", fmt.Errorf("db: %s()'s %s is not text go-db can read: %w", verb, what, err)
	}
	return text, nil
}

// mongoFlag reads a parsed boolean argument. The rendering of a boolean is
// exactly one of two words, so there is nothing to decode.
func mongoFlag(verb, what string, value mongoql.Value) (bool, error) {
	if value.Kind() != mongoql.KindBool {
		return false, fmt.Errorf("db: %s()'s %s must be true or false", verb, what)
	}
	return value.JSON() == "true", nil
}

// collectMongoDocuments drains a cursor into the Documents arm, stopping at
// MaxRows. It reads one document past the cap so Truncated means "there was
// more", not "you asked for exactly the cap" — the Table arm's collect makes
// the same peek, and for the same reason.
func collectMongoDocuments(ctx context.Context, cursor *mongo.Cursor) (Result, error) {
	documents := []json.RawMessage{}
	truncated := false

	for cursor.Next(ctx) {
		if len(documents) == MaxRows {
			truncated = true
			break
		}
		// Current is the cursor's own buffer, valid only until the next Next;
		// rendering it produces new bytes before that happens.
		rendered, err := renderMongoDocument(cursor.Current)
		if err != nil {
			return Result{}, err
		}
		documents = append(documents, rendered)
	}
	if err := cursor.Err(); err != nil {
		return Result{}, mongoError(err)
	}
	return DocumentsResult(documents, truncated), nil
}

// renderMongoDocument writes one document as the relaxed extended JSON the
// Documents arm carries. Every document in the arm comes through here, so the
// two that go-db renders itself — a count and a list of distinct values — are
// written by the same rules as the ones the server sent.
//
// HTML escaping is off. The bytes are JSON for a renderer, not markup, and a
// document holding a "<" would otherwise arrive as <, which is a different
// string from the one stored.
func renderMongoDocument(document any) (json.RawMessage, error) {
	rendered, err := bson.MarshalExtJSON(document, false, false)
	if err != nil {
		return nil, fmt.Errorf("db: rendering a document: %w", err)
	}
	return rendered, nil
}

// mongoError classifies a failure from the driver into one the caller can act
// on, the way classify does for MySQL: ErrAuthFailed when the server rejected
// the credentials, ErrUnreachable when it was never reached. Anything else
// keeps the server's own wording, which is usually the most useful thing we
// could say.
//
// The order matters, because the driver reports the two through different
// shapes. A rejected credential comes back as a server error carrying the
// server's code, all the way from inside the connection handshake. A server
// that was never reached comes back as a server-selection failure whose own
// cause is the deadline that ran out waiting for one — so the dial's refusal or
// its DNS failure is in the message rather than the chain, and what the chain
// carries is a timeout. That is enough for unreachable to recognise, and asking
// about the server error first is what keeps a rejected password from being
// caught by it.
func mongoError(err error) error {
	// A failure raised by the tunnel on the way out is already classified, and
	// names the hop that failed — which this adapter could not do, since it
	// does not know there is one. Its wording is the honest one; keep it.
	if errors.Is(err, ErrSSHAuthFailed) || errors.Is(err, ErrSSHHostKey) || errors.Is(err, ErrUnreachable) {
		return err
	}

	// A certificate this end would not accept, kept as its own outcome rather
	// than folded into either classified one. certificateRejected says why.
	if certificateRejected(err) {
		return fmt.Errorf("db: the server's TLS certificate was not accepted: %w", err)
	}

	if serverErr := mongo.ServerError(nil); errors.As(err, &serverErr) {
		if mongoRejectedCredentials(serverErr) {
			return fmt.Errorf("%w: %s", ErrAuthFailed, mongoMessage(err))
		}
		return fmt.Errorf("db: %s", mongoMessage(err))
	}

	if unreachable(err) {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return fmt.Errorf("db: %w", err)
}

// mongoRejectedCredentials reports whether the server said it reached the
// credentials and would not take them. It asks by error code rather than by
// message, because the code is the part of a MongoDB error that is specified.
func mongoRejectedCredentials(serverErr mongo.ServerError) bool {
	return serverErr.HasErrorCode(mongoAuthenticationFailed) || serverErr.HasErrorCode(mongoUserNotFound)
}

// mongoMessage is the server's own sentence about a failure. A command error
// carries one, and it is much shorter than the error's full text, which repeats
// the whole handshake it happened inside. Anything else is reported as it
// arrived.
func mongoMessage(err error) string {
	if commandErr := (mongo.CommandError{}); errors.As(err, &commandErr) && commandErr.Message != "" {
		return commandErr.Message
	}
	return err.Error()
}
