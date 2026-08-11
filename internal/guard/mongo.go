package guard

import (
	"strings"

	"github.com/themethaithian/go-db/internal/mongoql"
)

// ClassifyMongo decides whether one MongoDB statement is provably read-only.
//
// It is the MongoDB Engine's classifier (ADR-0006), and like the Redis one
// beside it there is nothing behind it. MySQL reads execute inside a READ ONLY
// transaction that refuses a write the classifier let through; MongoDB has no
// equivalent, so whatever this function calls a read runs unapproved with
// nothing to catch it.
//
// The read claim is made in four places, and all four are closed:
//
//   - The grammar. internal/mongoql accepts db.<collection>.<verb>(<args>) and
//     db.<verb>(<args>) and nothing else — not JavaScript, which cannot be
//     classified without being evaluated, and evaluating it is the hazard. A
//     statement that will not parse is a Mutation, which is also the answer for
//     SQL, a Redis command, a shell helper like "use db", and a buffer holding
//     two calls.
//
//   - mongoReads, the collection operations proven to only read. An operation
//     absent from it waits at the Approval Gate, including one MongoDB adds
//     after this table was written.
//
//   - mongoDatabaseReads, the same for the database-level form, and a separate
//     list rather than the same one because the two forms reach different
//     operations: db.find({}) is not a find on anything, and a name proven at
//     one level proves nothing at the other.
//
//   - mongoStages, for aggregate, whose argument is itself a list of
//     operations. $out and $merge write; every other stage is judged by the
//     same allowlist discipline, so an unknown stage is a Mutation rather than
//     an assumption.
//
// Arguments are not otherwise inspected, and that is a claim rather than an
// omission: apart from aggregate's pipeline, no argument to an operation on
// this list can turn it into a write. A filter's $where and $expr evaluate
// server-side, but the expression languages return values — they cannot issue
// an operation — so a find is a find whatever is in its filter.
func ClassifyMongo(statement string) Classification {
	if strings.TrimSpace(statement) == "" {
		return mutation("there is no statement to run")
	}

	call, err := mongoql.Parse(statement)
	if err != nil {
		return mutation(notTheGrammar + err.Error())
	}

	if call.OnDatabase() {
		return classifyDatabaseCall(call)
	}

	// The traps are consulted first only so that their wording wins. Being
	// listed there changes no verdict — absence from mongoReads is what makes
	// them mutations — and deleting an entry would lose the explanation rather
	// than the safety.
	if why, trapped := mongoTraps[call.Verb]; trapped {
		return mutation(call.Verb + "(), and " + why)
	}
	if call.Verb == "aggregate" {
		return classifyPipelineCall(call)
	}
	if mongoReads[call.Verb] {
		return read(call.Verb + "() query")
	}
	return mutation(call.Verb + "() is not on the list of MongoDB operations proven to only read")
}

// notTheGrammar opens the refusal of a statement that is not in the grammar at
// all. MongoDB is judged here — it is not an Engine go-db cannot judge — so the
// line says what go-db does read, and the parser's own line says what stopped
// it and where.
const notTheGrammar = "go-db reads MongoDB as db.<collection>.<verb>(...) and nothing else: "

// classifyDatabaseCall judges db.<verb>(...), the form that names no
// collection.
//
// It is a separate function over a separate list because the two forms are
// separate surfaces, and one shared list would be a way for a name proven at
// one level to be waved through at the other — db.find({}) is not a find, and
// db.users.getCollectionNames() is not a catalogue read.
//
// The arguments are not inspected, on the same ground the collection form
// states: the one operation on the list cannot be turned into a write by an
// argument. The adapter that runs it refuses arguments anyway, because
// getCollectionNames() takes none — but that is a question of running the call
// correctly, not of whether it writes.
func classifyDatabaseCall(call mongoql.Call) Classification {
	// Consulted first only so their wording wins, exactly as with the
	// collection traps: absence from mongoDatabaseReads is what makes these
	// mutations.
	if why, trapped := mongoDatabaseTraps[call.Verb]; trapped {
		return mutation("db." + call.Verb + "(), and " + why)
	}
	if mongoDatabaseReads[call.Verb] {
		return read("db." + call.Verb + "() query")
	}
	return mutation("db." + call.Verb + "() is not on the list of database-level MongoDB operations proven to only read")
}

// classifyPipelineCall judges an aggregate, whose verdict is its pipeline's.
//
// The second argument, if there is one, is the options document, and it is not
// inspected: an aggregation writes through a stage and through nothing else, so
// no option can turn a pipeline of reading stages into a write. A third
// argument is not part of the operation's shape at all, and a call go-db cannot
// read the shape of is a call it will not call a read.
func classifyPipelineCall(call mongoql.Call) Classification {
	switch {
	case len(call.Args) == 0:
		return mutation("aggregate() needs a pipeline, and only a pipeline whose every stage is proven to only read is a read")
	case len(call.Args) > 2:
		return mutation("aggregate() takes a pipeline and an options document, and this call has more arguments than that")
	}

	if refusal := judgeStages(call.Args[0]); refusal != nil {
		return *refusal
	}
	return read("aggregate() pipeline")
}

// judgeStages proves a pipeline reads, or says why it does not. It returns nil
// when every stage in it — and every stage in the subpipelines its stages carry
// — is on the allowlist.
//
// The shape is checked before the names are: a pipeline that is not an array of
// single-operator objects is one whose stages cannot be named with certainty,
// and a stage that cannot be named cannot be proved.
func judgeStages(pipeline mongoql.Value) *Classification {
	if pipeline.Kind() != mongoql.KindArray {
		return refuse("a pipeline must be written as an array of stages for go-db to judge what is in it")
	}

	for _, stage := range pipeline.Elements() {
		fields := stage.Fields()
		if stage.Kind() != mongoql.KindObject || len(fields) != 1 {
			return refuse("every stage in a pipeline must be an object holding exactly one operator, and one of these is not")
		}

		name := stageName(fields[0].Name)
		switch {
		case name == "":
			return refuse("one of the pipeline's stages is not on the list of aggregation stages proven to only read")
		case name == "$out":
			return refuse("the pipeline's $out stage writes its results into a collection, replacing what is there")
		case name == "$merge":
			return refuse("the pipeline's $merge stage writes its results into a collection")
		case !mongoStages[name]:
			return refuse(name + " is not on the list of aggregation stages proven to only read")
		}

		if refusal := judgeSubpipelines(name, fields[0].Value); refusal != nil {
			return refusal
		}
	}
	return nil
}

// judgeSubpipelines applies judgeStages to the pipelines a stage carries inside
// it, because a $out one level down writes exactly as much as a $out at the
// top.
//
// Three stages on the allowlist carry pipelines, and this function knows the
// shape of all three. That is what keeps the allowlist closed: a stage whose
// insides go-db does not know how to read is not on the list, so a future
// MongoDB stage carrying a pipeline is refused for being unknown before this
// function is ever asked about it. Where a stage's own shape is wrong, the
// answer is the same as everywhere else — go-db cannot read it, so it will not
// call it a read.
func judgeSubpipelines(stage string, argument mongoql.Value) *Classification {
	switch stage {
	case "$facet":
		// {$facet: {name: [stages…], …}} — every field is a subpipeline.
		if argument.Kind() != mongoql.KindObject {
			return refuse("the pipeline's $facet stage must be an object of subpipelines for go-db to judge what is in it")
		}
		for _, facet := range argument.Fields() {
			if refusal := judgeStages(facet.Value); refusal != nil {
				return refusal
			}
		}

	case "$lookup":
		// {$lookup: {from: …, pipeline: [stages…], as: …}} — the pipeline field
		// is optional, and the localField/foreignField form has none.
		if argument.Kind() != mongoql.KindObject {
			return refuse("the pipeline's $lookup stage must be an object for go-db to judge what is in it")
		}
		return judgeNamedSubpipeline(argument)

	case "$unionWith":
		// {$unionWith: "collection"} or {$unionWith: {coll: …, pipeline: […]}}.
		switch argument.Kind() {
		case mongoql.KindString:
			return nil
		case mongoql.KindObject:
			return judgeNamedSubpipeline(argument)
		}
		return refuse("the pipeline's $unionWith stage must name a collection or be an object for go-db to judge what is in it")
	}
	return nil
}

// judgeNamedSubpipeline judges the "pipeline" field of a stage that has one,
// and passes a stage that has none.
func judgeNamedSubpipeline(argument mongoql.Value) *Classification {
	for _, field := range argument.Fields() {
		if field.Name == "pipeline" {
			return judgeStages(field.Value)
		}
	}
	return nil
}

func refuse(reason string) *Classification {
	refusal := mutation(reason)
	return &refusal
}

// stageName renders an operator as the name a human would recognise, and
// returns "" for one that is not a name at all. A stage's operator can be
// written in quotes, which makes it any text the caller likes, and a rejected
// stage's name is quoted into the editor badge and the audit log. An unusable
// operator is refused either way — the reason simply stops naming it.
func stageName(operator string) string {
	const longestStageName = 40 // $setWindowFields, the longest here, is 16

	if operator == "" || len(operator) > longestStageName {
		return ""
	}
	for _, r := range operator {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '$', r == '_':
		default:
			return ""
		}
	}
	return operator
}

// mongoReads is the closed allowlist of collection operations proven to only
// read. Membership is the entire read claim for everything but aggregate, whose
// pipeline is judged by mongoStages below.
//
// Two rules govern what may be added:
//
//  1. The operation has no form that writes. MongoDB's CRUD API names its
//     writes separately — insertOne, updateMany, deleteOne, findOneAndDelete —
//     which is what makes an operation provable by its name at all, and is the
//     same property Redis's _RO commands give that classifier.
//
//  2. The operation cannot be turned into a write by an argument. This is why
//     aggregate is conditional rather than listed, and why mapReduce is absent
//     entirely: its out option names a collection to write.
//
// Scope is a client's day-to-day browsing: fetch documents, count them, list
// the values of a field. Read-only operations outside that — index
// introspection, the admin surface — are deliberately absent, because adding
// one later is cheap and removing one later is an incident. Listing a
// database's collections is the one catalogue read that has since been proven,
// and it lives at the level it belongs to: see mongoDatabaseReads.
var mongoReads = map[string]bool{
	// find returns a cursor over matching documents. Its options page, sort,
	// project and hint; none of them writes, and the write-side operations of
	// the CRUD API are separate names.
	"find": true,

	// findOne is find with a limit of one, and shares its options.
	"findOne": true,

	// countDocuments runs an aggregation of $match and $group over the filter
	// it is given. The pipeline is the driver's, not the caller's.
	"countDocuments": true,

	// estimatedDocumentCount reads the collection's own metadata and takes no
	// filter at all.
	"estimatedDocumentCount": true,

	// distinct returns the distinct values of one field, optionally filtered.
	"distinct": true,

	// aggregate is not listed here. It is a read only when every stage in its
	// pipeline is, which is a question about the argument rather than the name,
	// and ClassifyMongo routes it to classifyPipelineCall before this map is
	// consulted.
}

// mongoDatabaseReads is the closed allowlist of database-level operations
// proven to only read — the db.<verb>(...) form, which names no collection.
//
// It holds one entry, and the two rules governing mongoReads govern it: the
// operation must have no form that writes, and no argument may turn it into
// one. A third applies here and nowhere else, because this is the level the
// admin surface lives on: the operation must be something a client browsing a
// database needs. Everything else at this level — the command runners, the
// database's own statistics, the session and shard surface — is absent, and the
// tempting ones are named in mongoDatabaseTraps rather than merely left out.
var mongoDatabaseReads = map[string]bool{
	// getCollectionNames is mongosh's name for listCollections with
	// nameOnly: true. The proof is what that command is: listCollections reads
	// the database's own catalogue and returns an entry per collection, and
	// nameOnly asks the server for nothing but the names — no statistics, no
	// options, no index information, and nothing that touches a collection's
	// documents. It has no writing form (a collection is created by
	// createCollection or by writing to it), takes no argument in mongosh, and
	// the filter the underlying command accepts selects which entries come back
	// rather than doing anything to them. It is the catalogue equivalent of
	// MySQL's SELECT over information_schema.tables, which is what the Database
	// tree asks every other Engine.
	"getCollectionNames": true,
}

// mongoDatabaseTraps is mongoTraps for the database-level form: operations that
// would otherwise be refused with nothing but "not on the list", and what the
// human missed. Each value completes the sentence "db.<verb>(), and ...".
//
// Being listed here changes no verdict — absence from mongoDatabaseReads is
// what makes these mutations — and deleting an entry would lose the
// explanation rather than the safety.
var mongoDatabaseTraps = map[string]string{
	// Writes, wearing a database-level name.
	"dropDatabase":     "it deletes the database and everything in it",
	"createCollection": "it creates a collection, which is a change to the database",
	"createView":       "it creates a view, which is a change to the database",

	// The command runners. Each takes a command document naming an operation
	// go-db has not read, which is the whole difficulty: judging one would mean
	// classifying every command MongoDB has, from inside an argument.
	"runCommand":   commandRunner,
	"adminCommand": commandRunner,
	"aggregate":    "a database-level aggregation can name any collection from inside its pipeline; db.<collection>.aggregate([...]) is the form go-db judges",

	// Reaching another database. go-db pins the database a Profile connects to,
	// and the leading db is that database — a statement that could move to
	// another one would run somewhere the Profile does not name.
	"getSiblingDB":  "it moves to another database, and a go-db Profile names the one database its statements run in",
	"getMongo":      "it reaches the connection itself, and go-db's MongoDB grammar works inside one database",
	"getCollection": "it names a collection through a helper, and go-db's grammar names one plainly, as in db.users.find({})",

	// Reads on the admin surface. They read, but each would need its own
	// argument read and its own proof, and none has one yet.
	"getCollectionInfos": "it returns every collection's full definition; getCollectionNames() is the catalogue read proven to only read",
	"stats":              databaseAdminSurface,
	"serverStatus":       databaseAdminSurface,
	"hostInfo":           databaseAdminSurface,
	"currentOp":          databaseAdminSurface,
	"killOp":             "it stops an operation somebody else is running, and " + databaseAdminSurface,
	"getUsers":           databaseAdminSurface,
	"getRoles":           databaseAdminSurface,

	// watch, one level up from the collection trap of the same name.
	"watch": "it opens a change stream and holds the connection open, waiting for changes that have not happened yet",
}

const (
	commandRunner        = "it runs a command named inside its argument, and go-db classifies operations by reading them rather than by trusting what they are called"
	databaseAdminSurface = "database administration is not on the list of MongoDB operations proven to only read"
)

// mongoStages is the closed allowlist of aggregation stages proven to only
// read: a pipeline is a read when every one of its stages is here, and a stage
// that is not is a Mutation whether or not it writes.
//
// Allowlisting rather than blocklisting the two writers is the whole discipline
// (ADR-0006). $out and $merge are the only stages that write today, and a
// blocklist of the two would inherit every writing stage MongoDB adds after
// this table was written — silently, and on the side of running it.
//
// Stages that are read-only but outside a client's browsing — $collStats,
// $indexStats, $planCacheStats, $listSessions, $changeStream, $documents — are
// absent for the same reason the Redis table leaves out its exotica.
var mongoStages = map[string]bool{
	// Selecting and reshaping the documents flowing through the pipeline. The
	// aggregation $set is not the update $set: it adds fields to the documents
	// on their way past, and where they go is decided by the pipeline's last
	// stage, which is why $out and $merge are the stages that matter.
	"$match":       true,
	"$project":     true,
	"$addFields":   true,
	"$set":         true,
	"$unset":       true,
	"$replaceRoot": true,
	"$replaceWith": true,
	"$redact":      true,
	"$unwind":      true,

	// Grouping and counting.
	"$group":       true,
	"$count":       true,
	"$sortByCount": true,
	"$bucket":      true,
	"$bucketAuto":  true,

	// Ordering, paging and sampling.
	"$sort":   true,
	"$limit":  true,
	"$skip":   true,
	"$sample": true,

	// Joining. The three stages that carry subpipelines are here, and their
	// insides are judged by the same list — see judgeSubpipelines.
	"$lookup":      true,
	"$graphLookup": true,
	"$unionWith":   true,
	"$facet":       true,

	// Geo. $geoNear reads an index and sorts by distance.
	"$geoNear": true,
}

// mongoTraps names the operations that would otherwise be refused with nothing
// but "not on the list", and says what the human missed. Every one of them is
// an operation a reasonable person would call a read.
//
// Each value completes the sentence "<verb>(), and ...".
var mongoTraps = map[string]string{
	// Reads that write, or can be made to.
	"mapReduce": "its out option writes the results into a collection",
	"count":     "it is deprecated and counts differently between server versions; countDocuments() is the form proven to only read",

	// The find-and-write family. Every one of them reads a document and
	// changes it in the same operation, which is the point of them.
	"findAndModify":     findAndWrites,
	"findOneAndUpdate":  findAndWrites,
	"findOneAndReplace": findAndWrites,
	"findOneAndDelete":  findAndWrites,

	// watch opens a change stream and holds the connection open until it is
	// closed, on a connection the editor shares — the same ground SUBSCRIBE is
	// off the Redis list on.
	"watch": "it opens a change stream and holds the connection open, waiting for changes that have not happened yet",

	// explain() returns an object to chain the real operation onto, and go-db's
	// MongoDB grammar accepts one call and no chaining.
	"explain": "it is the start of a chained call, and go-db reads MongoDB as one db.<collection>.<verb>(...) call",

	// The admin surface. These read, but they are a different surface from
	// browsing documents, and nothing on it has been proved yet: validate can
	// be asked to repair, and the rest would each need their own argument read.
	"stats":                adminSurface,
	"validate":             "it can be asked to repair the collection it checks, and " + adminSurface,
	"getIndexes":           adminSurface,
	"getShardDistribution": adminSurface,
}

const (
	findAndWrites = "it writes the document it finds in the same operation; find() and findOne() are the forms proven to only read"
	adminSurface  = "collection administration is not on the list of MongoDB operations proven to only read"
)
