package guard

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
)

// StatementSpan is where one statement sits in the text it was split out of.
//
// Start and End are byte offsets into that text, End exclusive, and Text is
// always exactly input[Start:End] — a span is a pointer into the buffer, not a
// reformatting of it. They are byte offsets into UTF-8, not character or UTF-16
// offsets, so an adapter handing them to an editor that counts code units has
// to convert.
//
// A span covers its statement's trailing semicolon when it has one, and stops
// short of the whitespace around it.
type StatementSpan struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// SplitStatements finds the statements in sql and returns where each one
// begins and ends, in order and without overlapping.
//
// It is what the editor's run-at-cursor is built on. The human keeps several
// statements in one buffer, puts the cursor in one of them, and exactly one
// span — the one containing the cursor — is what gets classified, previewed and
// submitted. Nothing else in the buffer is sent anywhere.
//
// The boundaries are the parser's and never a scan for semicolons: a semicolon
// inside a string literal or a comment does not end a statement, and only
// something that reads MySQL's grammar knows the difference. This is the same
// reason the statement text is taken from the parser rather than reconstructed.
//
// A comment written above a statement stays inside that statement's span. It is
// how the human labels the statement, so a cursor resting on the comment finds
// the statement it belongs to rather than nothing. A comment trailing the last
// statement belongs to no statement and is in no span.
//
// Input holding no statement at all — empty, whitespace, comments, stray
// semicolons — has no spans. The result is never nil, so an adapter serialising
// it hands the UI an empty list rather than a null.
//
// # Input that will not parse
//
// Most of the time the buffer does not parse, because the human is still
// typing. Splitting therefore fails closed but usefully: the leading statements
// that do parse come back as themselves, and everything from the first thing
// the parser choked on to the end of the input becomes one final span.
//
// That last span is deliberately the whole remainder rather than a guess at
// where the broken statement ends. Guessing would mean splitting on semicolons
// after all, in exactly the case where nothing has been proven about them. The
// remainder is still a span so the editor can target it and show something, and
// what it shows is safe: Classify reads it as unparseable, or as more than one
// statement, and either way calls it a Mutation, so a half-written statement
// never runs as a read. That is the contract — a span is a place in the buffer,
// not a claim that what is there is a statement.
func SplitStatements(sql string) (spans []StatementSpan) {
	// The parser is a large generated state machine fed untrusted text, and the
	// editor feeds it a half-written buffer on every keystroke. A panic in it
	// must not lose the human's place in their own query, so the recovery
	// produces the same fail-closed answer as unparseable input: one span over
	// everything, which the classifier will withhold.
	defer func() {
		if recovered := recover(); recovered != nil {
			spans = remainder(sql, 0)
		}
	}()

	if texts, err := statementTexts(sql); err == nil {
		if spans, ok := spansFor(sql, texts); ok {
			return spans
		}
		return remainder(sql, 0)
	}

	texts, cut := parseableLead(sql)
	spans, ok := spansFor(sql, texts)
	if !ok {
		return remainder(sql, 0)
	}
	return append(spans, remainder(sql, cut)...)
}

// statementTexts parses sql and returns each statement's text exactly as the
// parser sliced it out of the input.
//
// The borrow follows the same rule Classify's does, for the same reason. The
// strings themselves are substrings of sql and stay valid for as long as the
// caller's own argument does, but the slice holding them is the parser's and is
// truncated and refilled on its next use — so it is copied out before the
// parser goes back in the pool, and a parser that panicked is not put back at
// all.
func statementTexts(sql string) ([]string, error) {
	p := parsers.Get().(*parser.Parser)
	stmts, _, err := p.Parse(sql, "", "")

	var texts []string
	if err == nil {
		texts = make([]string, len(stmts))
		for i, stmt := range stmts {
			// OriginalText and not Text: Text re-encodes some string literals,
			// and a span has to hand back the input unchanged.
			texts[i] = stmt.OriginalText()
		}
	}

	parsers.Put(p)
	return texts, err
}

// spansFor locates each statement text in sql and trims it down to the
// statement. ok is false when a text is not found where it should be, which
// means the parser has stopped handing back slices of what it was given; no
// offset here could then be trusted to point at the right statement, so the
// caller falls back rather than guessing.
//
// Each text is searched for from where the last one ended rather than assumed
// to start there: the lexer trims a newline off the end of a statement before
// recording it, so consecutive texts can be a byte apart.
func spansFor(sql string, texts []string) ([]StatementSpan, bool) {
	spans := make([]StatementSpan, 0, len(texts))
	cursor := 0
	for _, text := range texts {
		offset := strings.Index(sql[cursor:], text)
		if offset < 0 {
			return nil, false
		}
		start := cursor + offset
		cursor = start + len(text)
		if span, ok := trimSpan(sql, start, cursor); ok {
			spans = append(spans, span)
		}
	}
	return spans, true
}

// parseableLead finds the longest prefix of sql that ends at a semicolon and
// parses, and returns its statements' texts along with the offset just past
// that semicolon. It answers nothing when no prefix parses, which leaves the
// whole input to the caller's final span.
//
// Candidates are tried longest first, and that ordering is the point. A
// semicolon inside a line comment ends a prefix that parses perfectly well —
// "SELECT 1 -- ;" is a whole SELECT — so taking the shortest cut would saw a
// statement in half at a semicolon that was never a boundary. The longest cut
// that parses takes the comment with it.
//
// The scan is bounded because every candidate costs a parse of the text before
// it and the editor splits on every keystroke. The bound is generous compared
// with the case that matters — a buffer of finished statements with a
// half-typed one at the end succeeds on the first candidate — and past it the
// input is broken enough that calling the whole thing one span, and letting the
// gate withhold it, is the more honest answer anyway.
func parseableLead(sql string) ([]string, int) {
	end := len(sql)
	for attempt := 0; attempt < maxSplitAttempts; attempt++ {
		semicolon := strings.LastIndexByte(sql[:end], ';')
		if semicolon < 0 {
			return nil, 0
		}
		if texts, err := statementTexts(sql[:semicolon+1]); err == nil && len(texts) > 0 {
			return texts, semicolon + 1
		}
		end = semicolon
	}
	return nil, 0
}

const maxSplitAttempts = 64

// remainder is the span the splitter falls back to: everything from offset to
// the end of the input, trimmed. It is a slice rather than a value so that a
// remainder of nothing but whitespace adds nothing.
func remainder(sql string, offset int) []StatementSpan {
	if span, ok := trimSpan(sql, offset, len(sql)); ok {
		return []StatementSpan{span}
	}
	return []StatementSpan{}
}

// trimSpan narrows [start, end) onto the statement itself and reports whether
// anything was left. The whitespace around a statement is not part of it, and
// neither are the semicolons in front of it — those belong to the empty
// statements someone typed by holding the key down, and a statement submitted
// with one still attached is a syntax error at the server. The semicolon at the
// end is kept: that one is the statement's own.
func trimSpan(sql string, start, end int) (StatementSpan, bool) {
	text := strings.TrimLeft(sql[start:end], leadingNoise)
	start = end - len(text)

	text = strings.TrimRight(text, whitespace)
	if text == "" {
		return StatementSpan{}, false
	}
	return StatementSpan{Start: start, End: start + len(text), Text: text}, true
}

// whitespace is the set MySQL's lexer skips between tokens. Unicode spaces are
// not in it, because they are not in the lexer's either.
const whitespace = " \t\n\v\f\r"

const leadingNoise = whitespace + ";"
