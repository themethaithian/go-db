package service

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
)

// This file is where the facade branches on the Engine, and the only place it
// does (ADR-0006). Everything else about a query — the gate, both Origin
// policies, the Connection Registry, the audit log — is Engine-agnostic and
// stays that way; what an Engine decides is the language a statement is written
// in, and so which classifier judges it, where one statement ends, and whether
// there is a preview of it.
//
// guard keeps one exported classifier per Engine and no dispatcher, because
// dispatching means knowing what Engines there are, and Engine is db's word.
// This is the seam where the two meet.

// classifyFor judges statement with the classifier the Engine names.
//
// Every Engine go-db knows now has one, so the default arm is reached only by
// something that is not an Engine at all — an unset one, or a word somebody put
// in profiles.toml by hand. It stays load-bearing for exactly that: nothing in
// this app can say what such a statement does, so nothing may run it without a
// human saying so. That is the gate's own posture, applied one level up, and it
// is also where the next Engine lands before its classifier is written.
func classifyFor(engine db.Engine, statement string) guard.Classification {
	switch engine {
	case db.EngineMySQL:
		return guard.Classify(statement)
	case db.EngineRedis:
		return guard.ClassifyRedis(statement)
	case db.EngineMongoDB:
		return guard.ClassifyMongo(statement)
	default:
		return guard.Unjudged(cannotJudge(engine))
	}
}

// splitFor finds the statements in buffer, the way the Engine's language
// separates them.
//
// Only SQL is split. A Redis command line is one command, ended by the newline
// rather than by a separator inside the text, so the whole trimmed buffer is
// one span — and that is honest rather than lax: the classifier refuses a
// buffer holding an embedded newline outright, so a span covering two commands
// is a span the gate will withhold. MongoDB's grammar reaches the same answer
// from the other direction: it is one call per buffer by definition, newlines
// inside it and all, and a buffer holding a second call is refused whole rather
// than split into two. Every other Engine takes the same answer again, since a
// splitter guessing at boundaries in a grammar nobody has implemented would be
// inventing them.
func splitFor(engine db.Engine, buffer string) []guard.StatementSpan {
	if engine == db.EngineMySQL {
		return guard.SplitStatements(buffer)
	}
	return wholeBuffer(buffer)
}

// wholeBuffer is buffer as one span, trimmed of the whitespace around it. The
// span is a place in the text and not a claim about what is there — the same
// contract SplitStatements works to — so its offsets index the caller's own
// buffer and its text is exactly what lies between them.
//
// A buffer with nothing in it has no span, which is how the editor is told
// there is nothing to run.
func wholeBuffer(buffer string) []guard.StatementSpan {
	trimmed := strings.TrimLeftFunc(buffer, unicode.IsSpace)
	start := len(buffer) - len(trimmed)

	text := strings.TrimRightFunc(trimmed, unicode.IsSpace)
	if text == "" {
		return []guard.StatementSpan{}
	}
	return []guard.StatementSpan{{Start: start, End: start + len(text), Text: text}}
}

// pageWindowFor reads the window of rows a statement asks for, and repageFor
// moves it, in the language the Engine's answers come back in.
//
// Only MySQL is paged. A window is LIMIT and OFFSET over a table of rows, and
// that is what a SQL result is; a Redis reply is one typed value, with no rows
// to take a second screenful of, and MongoDB's own skip and limit are the
// Engine's to add when its editor paging is written. Both refuse in the shape
// every refusal here takes — no window, and a line saying why — so the editor
// hides its pager rather than drawing one that cannot move.
//
// The default page size is db.MaxRows, and it is supplied here rather than in
// guard because internal/db imports guard and not the other way round. It has
// to be the cap that really bounded the result: a statement with no LIMIT
// returned exactly that many rows, and the next page starts after them.
func pageWindowFor(engine db.Engine, statement string) guard.Window {
	if engine == db.EngineMySQL {
		return guard.PageWindow(statement, db.MaxRows)
	}
	return guard.NoWindow(cannotPage(engine))
}

func repageFor(engine db.Engine, statement string, size, offset int64) guard.Window {
	if engine == db.EngineMySQL {
		return guard.Repage(statement, size, offset)
	}
	return guard.NoWindow(cannotPage(engine))
}

// cannotPage says why an Engine's answer has no next page, for the human whose
// pager is not there.
func cannotPage(engine db.Engine) string {
	if !engine.Valid() {
		return fmt.Sprintf("go-db does not know the %q engine this Profile names, so it cannot page its answers", engine)
	}
	return fmt.Sprintf("go-db does not page %s answers yet", engineName(engine))
}

// engineFor resolves the Engine of the Profile a query names.
//
// The Engine comes from the saved Profile and never from the caller, and that
// is the whole reason this is a lookup rather than a parameter: an Origin that
// could name the Engine its statement is judged by could name the classifier
// that says yes. ProfileStore has already normalized what it holds, so the
// Engine that comes back is the one every other part of the app sees for that
// Profile.
func (s *AppService) engineFor(profileName string) (db.Engine, error) {
	profile, err := s.profiles.Get(profileName)
	if err != nil {
		return "", err
	}
	return profile.Engine, nil
}

// noEngine is why a statement was not judged when the Profile it named is not
// there to name an Engine.
const noEngine = "go-db cannot tell what this statement is: there is no Profile to say what language it is written in"

// cannotJudge says why a statement was not judged, for the human reading the
// confirmation it raised. Every Engine go-db knows has a classifier, so the
// second line is written for the next one: an Engine added to db before the
// classifier that judges it, which is a Mutation in the meantime and says so.
func cannotJudge(engine db.Engine) string {
	if !engine.Valid() {
		return fmt.Sprintf("go-db does not know the %q engine this Profile names, so it cannot tell what this statement does", engine)
	}
	return fmt.Sprintf("go-db cannot judge %s statements yet", engineName(engine))
}

// noPreviewFor says why a mutation on this Engine has no Impact Preview.
//
// ADR-0006 gives Redis and MongoDB previews of their own — DEL rewritten to
// EXISTS, a Mongo filter counted with countDocuments — and neither is written.
// Until they are, the answer is the established one rather than a new state:
// no preview, and it says so.
func noPreviewFor(engine db.Engine) string {
	if !engine.Valid() {
		return fmt.Sprintf("go-db does not know the %q engine this Profile names", engine)
	}
	return fmt.Sprintf("go-db does not preview %s statements yet", engineName(engine))
}

// cannotIntrospect says why the Database tree cannot ask this Engine for
// something — columns of a Redis key, indexes of a MongoDB collection, tables
// of an Engine go-db has no adapter for.
//
// It is a refusal rather than an empty answer on purpose. An empty list is a
// real thing a database can report, so answering one here would say "this table
// has no columns" where the truth is "this is not a thing with columns", and the
// tree would draw the lie. what is the plural noun that was asked for, so the
// sentence names the question rather than the caller.
func cannotIntrospect(engine db.Engine, what string) string {
	if !engine.Valid() {
		return fmt.Sprintf("go-db does not know the %q engine this Profile names, so it cannot list its %s.", engine, what)
	}
	return fmt.Sprintf("A %s Profile has no %s for go-db to list.", engineName(engine), what)
}

// engineName is an Engine as a human writes it, for the middle of a sentence.
func engineName(engine db.Engine) string {
	switch engine {
	case db.EngineMySQL:
		return "MySQL"
	case db.EngineRedis:
		return "Redis"
	case db.EngineMongoDB:
		return "MongoDB"
	default:
		return fmt.Sprintf("%q", engine)
	}
}
