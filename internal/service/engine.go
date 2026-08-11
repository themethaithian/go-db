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
// The default arm is the load-bearing one. An Engine reaches it two ways: it is
// one go-db knows and has no classifier for yet (MongoDB, whose restricted
// grammar ADR-0006 specifies and nobody has written), or it is not an Engine at
// all — an unset one, or a word somebody put in profiles.toml by hand. Both are
// the same situation from here: nothing in this app can say what the statement
// does, so nothing may run it without a human saying so. That is the gate's own
// posture, applied one level up.
func classifyFor(engine db.Engine, statement string) guard.Classification {
	switch engine {
	case db.EngineMySQL:
		return guard.Classify(statement)
	case db.EngineRedis:
		return guard.ClassifyRedis(statement)
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
// is a span the gate will withhold. Every other Engine takes the same answer
// for the same reason, since a splitter that guessed at boundaries in a grammar
// nobody has implemented would be inventing them.
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
// confirmation it raised.
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
