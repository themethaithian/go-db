// Package mongoql parses the two statement shapes go-db accepts for MongoDB:
//
//	db.<collection>.<verb>(<arguments>)
//	db.<verb>(<arguments>)
//
// It is not JavaScript, and that is the whole point (ADR-0006). mongosh's
// language cannot be classified without evaluating it, and evaluating it is the
// hazard the Approval Gate exists to hold back, so go-db reads a grammar small
// enough to judge by looking at it. Everything the grammar leaves out is left
// out on purpose, and every refusal below says what to write instead where
// there is an answer.
//
// The second shape is an operation on the database itself rather than on one of
// its collections — db.getCollectionNames() and nothing else worth writing here,
// since which database-level operations may run is the classifier's list and not
// the grammar's. The parser only has to tell the two shapes apart, and it does
// so by the one character that distinguishes them: after db.<name>, a "(" makes
// the name an operation on the database, and a "." makes it a collection.
//
// The accepted grammar, in full:
//
//	statement   = ws , "db" , ws , "." , ws , ( collection call | database call ) ,
//	              ws , [ ";" , ws ] ;
//	collection call = collection , ws , "." , ws , verb , ws , "(" , arguments , ")" ;
//	database call   = verb , ws , "(" , arguments , ")" ;
//	collection  = identifier ;
//	verb        = identifier ;
//	identifier  = ( letter | "_" ) , { letter | digit | "_" } ;
//	arguments   = ws , [ value , { ws , "," , ws , value } , ws ] ;
//	value       = object | array | string | number | "true" | "false" | "null" ;
//	object      = "{" , ws , [ member , { ws , "," , ws , member } , ws ] , "}" ;
//	member      = key , ws , ":" , ws , value ;
//	key         = identifier$ | string ;
//	identifier$ = ( letter | "_" | "$" ) , { letter | digit | "_" | "$" } ;
//	array       = "[" , ws , [ value , { ws , "," , ws , value } , ws ] , "]" ;
//	string      = '"' , { char } , '"' | "'" , { char } , "'" ;
//	number      = [ "-" ] , ( "0" | digit1-9 , { digit } ) ,
//	              [ "." , digit , { digit } ] ,
//	              [ ( "e" | "E" ) , [ "+" | "-" ] , digit , { digit } ] ;
//	ws          = { space | tab | newline | carriage return } ;
//
// What that leaves out, and why:
//
//   - One call per buffer. Nothing may precede db and nothing may follow the
//     closing parenthesis but whitespace and at most one semicolon — so no
//     chained cursor helper (.limit(1)), no second call on a second line, and
//     no second call hidden after the first. Strings and nesting are scanned
//     properly rather than matched with a pattern, so a ")" inside a quoted
//     string does not end the call and a call inside one is only text.
//
//   - One plain collection name. db["x"] and dotted names are refused: each is
//     another way to name a collection, and each would be another shape to be
//     right about. db.getCollection("x") parses as a database-level call — it
//     is one, syntactically — and is refused the moment anything is chained
//     onto it, in the words of the collection form it was reaching for.
//
//   - JSON values only. No function calls, so no ObjectId(...), ISODate(...) or
//     NumberLong(...) — MongoDB's extended JSON writes all of those as ordinary
//     objects, {"$oid": "…"} and {"$date": "…"}, which this grammar parses as
//     the plain objects they are and passes through untouched. No bare
//     identifiers, so no variables and no undefined or NaN. No comments, no
//     template literals, no regular-expression literals, no arithmetic. Keys
//     may be written unquoted and strings may be single-quoted, because mongosh
//     users write them that way and neither needs evaluating to read.
//
//   - Bounded input. A statement over 64 KB is refused unread, and values may
//     nest 32 deep. The parser is recursive descent, so without a depth bound
//     the input would choose how much stack to use; 32 is far above anything a
//     person writes — MongoDB's own document nesting limit is 100 — and far
//     below anything that could exhaust a goroutine stack.
//
// Refusals name a position and say what was wrong. They never echo the input
// back: a refusal is quoted into the editor badge and the audit log, and the
// text that caused it was written by whoever submitted the statement.
//
// This package imports nothing but the standard library, which is what lets
// both internal/guard (to classify a call) and, later, internal/db (to run one)
// depend on it.
package mongoql

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Call is one statement in the grammar: an operation on one collection or on
// the database itself, with the arguments it was given, in order.
//
// Collection and Verb are exactly as written — MongoDB is case-sensitive about
// both, so nothing is folded. Args is nil for a call written with empty
// parentheses.
//
// Which of the two forms a Call is, is Collection: a database-level call names
// no collection, and its Collection is empty. That is the representation rather
// than a flag beside it, and deliberately so — a flag is a second copy of a fact
// the field already carries, and two copies can disagree. The grammar makes the
// reading safe: a collection is an identifier, an identifier is at least one
// character, so an empty Collection is a thing the collection form cannot
// produce. OnDatabase is how callers ask, so the reasoning above lives in one
// place instead of at every comparison against "".
type Call struct {
	Collection string
	Verb       string
	Args       []Value
}

// OnDatabase reports whether this call is the database-level form,
// db.<verb>(...), which names no collection.
//
// Every caller that runs a Call must ask: the two forms reach different
// operations on the server, and a database-level verb handed to a collection is
// an operation on a collection with no name.
func (c Call) OnDatabase() bool { return c.Collection == "" }

const (
	// maxStatementBytes is generous by design: a pipeline with a large $in list
	// is a few kilobytes, and nothing a person types approaches this. It exists
	// so that a paste of a log file is refused before it is scanned.
	maxStatementBytes = 64 << 10

	// maxDepth bounds recursion. See the package comment.
	maxDepth = 32

	// maxIdentifier bounds a collection or operation name. MongoDB's own
	// namespace limit is 255 bytes for database, dot and collection together,
	// so a name longer than this could not exist on the server it would be sent
	// to.
	maxIdentifier = 128
)

// Parse reads statement as exactly one call in the grammar above.
//
// The error is one line of prose fit to show a human, ending in the character
// position the parser stopped at. There is no partial success: a statement is
// the shape the grammar names or it is refused, and the Approval Gate turns
// every refusal into a Mutation.
func Parse(statement string) (Call, error) {
	if len(statement) > maxStatementBytes {
		return Call{}, errors.New("the statement is too long for go-db to read as one MongoDB call")
	}
	if !utf8.ValidString(statement) {
		return Call{}, errors.New("the statement is not valid UTF-8 text")
	}

	p := &parser{src: statement}
	return p.parseCall()
}

// parser scans statement text on demand: there is no separate token stream,
// because every position in this grammar admits so few tokens that asking for
// the one that belongs there is clearer than producing all of them first. What
// matters — that strings and nesting are read as strings and nesting rather
// than matched with a pattern — is the same either way.
type parser struct {
	src   string
	pos   int // byte offset of the next unread byte
	depth int // objects and arrays currently open
}

func (p *parser) parseCall() (Call, error) {
	p.skipSpace()
	if p.atEnd() {
		return Call{}, p.errorAt(p.pos, "there is nothing to run")
	}

	start := p.pos
	if p.scanWord() != "db" {
		return Call{}, p.errorAt(start, "a MongoDB statement starts with db, as in db.users.find({})")
	}

	p.skipSpace()
	if p.peek() != '.' {
		return Call{}, p.errorAt(p.pos, "after db go-db expects .<collection>.<operation>(...), and its MongoDB grammar has no other way to name a collection")
	}
	p.pos++

	// The one name both forms start with: a collection when a dot follows it, an
	// operation on the database when a bracket does. Nothing before this point
	// can tell them apart, and nothing after it has to guess.
	p.skipSpace()
	name, err := p.identifier("collection")
	if err != nil {
		return Call{}, err
	}

	p.skipSpace()
	switch p.peek() {
	case '(':
		return p.finishCall("", name)
	case '.':
		p.pos++
	default:
		return Call{}, p.errorAt(p.pos, "after the collection go-db expects .<operation>(...), on one plain collection name")
	}

	p.skipSpace()
	verb, err := p.identifier("operation")
	if err != nil {
		return Call{}, err
	}

	p.skipSpace()
	if p.peek() != '(' {
		return Call{}, p.errorAt(p.pos, "the operation must be called, as in db.users.find({}), and its arguments go in the brackets")
	}
	return p.finishCall(name, verb)
}

// finishCall reads the argument list of either form and the punctuation after
// it, with the opening bracket unread. collection is empty for the
// database-level form.
func (p *parser) finishCall(collection, verb string) (Call, error) {
	p.pos++ // '('

	args, err := p.arguments()
	if err != nil {
		return Call{}, err
	}

	// One call, exactly. Whitespace and a single semicolon are the editor's
	// punctuation; anything else after the closing bracket is a second thing to
	// run, and the whole buffer is refused rather than judged on its first call.
	p.skipSpace()
	if p.peek() == ';' {
		p.pos++
		p.skipSpace()
	}
	if !p.atEnd() {
		// A dot after a database-level call is db.getCollection("x").find(...)
		// and its relatives: a helper that names a collection and then works on
		// it. It is refused for chaining like anything else, but the human is
		// told the thing they actually need to know.
		if collection == "" && p.peek() == '.' {
			return Call{}, p.errorAt(p.pos, "the collection must be a plain name, as in db.users.find(...): the grammar has no collection helpers and no chaining")
		}
		return Call{}, p.errorAt(p.pos, "go-db runs one MongoDB call at a time, and there is more here after the first one")
	}
	return Call{Collection: collection, Verb: verb, Args: args}, nil
}

// arguments reads the argument list, the opening bracket already consumed, and
// consumes the closing one.
func (p *parser) arguments() ([]Value, error) {
	p.skipSpace()
	if p.peek() == ')' {
		p.pos++
		return nil, nil
	}

	var args []Value
	for {
		arg, err := p.value()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)

		p.skipSpace()
		switch p.peek() {
		case ',':
			p.pos++
			p.skipSpace()
			if p.peek() == ')' {
				return nil, p.errorAt(p.pos, "there is a comma with no argument after it")
			}
		case ')':
			p.pos++
			return args, nil
		default:
			return nil, p.errorAt(p.pos, "go-db expects a comma between arguments and a ) after the last one")
		}
	}
}

func (p *parser) value() (Value, error) {
	c := p.peek()
	switch {
	case c < 0:
		return Value{}, p.errorAt(p.pos, "the statement ends where a value should be")
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"' || c == '\'':
		return p.text()
	case c == '-' || isDigit(byte(c)):
		return p.number()
	case isWordChar(byte(c)):
		return p.word()
	}
	return Value{}, p.errorAt(p.pos, "go-db expects a JSON value here, and found "+describe(c))
}

func (p *parser) object() (Value, error) {
	if err := p.descend(); err != nil {
		return Value{}, err
	}
	defer func() { p.depth-- }()

	p.pos++ // '{'
	p.skipSpace()
	if p.peek() == '}' {
		p.pos++
		return Value{kind: KindObject}, nil
	}

	var fields []Field
	for {
		name, err := p.key()
		if err != nil {
			return Value{}, err
		}

		p.skipSpace()
		if p.peek() != ':' {
			return Value{}, p.errorAt(p.pos, "go-db expects a : after a field name")
		}
		p.pos++

		p.skipSpace()
		value, err := p.value()
		if err != nil {
			return Value{}, err
		}
		fields = append(fields, Field{Name: name, Value: value})

		p.skipSpace()
		switch p.peek() {
		case ',':
			p.pos++
			p.skipSpace()
			if p.peek() == '}' {
				return Value{}, p.errorAt(p.pos, "there is a comma with no field after it")
			}
		case '}':
			p.pos++
			return Value{kind: KindObject, fields: fields}, nil
		default:
			return Value{}, p.errorAt(p.pos, "go-db expects a comma between fields and a } after the last one")
		}
	}
}

func (p *parser) array() (Value, error) {
	if err := p.descend(); err != nil {
		return Value{}, err
	}
	defer func() { p.depth-- }()

	p.pos++ // '['
	p.skipSpace()
	if p.peek() == ']' {
		p.pos++
		return Value{kind: KindArray, elements: []Value{}}, nil
	}

	elements := []Value{}
	for {
		element, err := p.value()
		if err != nil {
			return Value{}, err
		}
		elements = append(elements, element)

		p.skipSpace()
		switch p.peek() {
		case ',':
			p.pos++
			p.skipSpace()
			if p.peek() == ']' {
				return Value{}, p.errorAt(p.pos, "there is a comma with no element after it")
			}
		case ']':
			p.pos++
			return Value{kind: KindArray, elements: elements}, nil
		default:
			return Value{}, p.errorAt(p.pos, "go-db expects a comma between elements and a ] after the last one")
		}
	}
}

// key reads a field name, quoted or not. Unquoted names may hold a dollar, so
// that {$gte: 1} and {$oid: "…"} can be written the way mongosh writes them; a
// name holding anything else — a dot, a hyphen, a space — has to be quoted, as
// it does in JavaScript.
func (p *parser) key() (string, error) {
	if c := p.peek(); c == '"' || c == '\'' {
		quoted, err := p.text()
		if err != nil {
			return "", err
		}
		return quoted.text, nil
	}

	start := p.pos
	if name := p.scanWord(); name != "" {
		if len(name) > maxIdentifier {
			return "", p.errorAt(start, "that field name is too long")
		}
		return name, nil
	}
	return "", p.errorAt(p.pos, "go-db expects a field name here, written plainly or in quotes")
}

// identifier reads a collection or operation name. Neither may hold a dollar:
// the grammar names one collection and one operation, and the dollar belongs to
// MongoDB's operators rather than to either.
func (p *parser) identifier(what string) (string, error) {
	start := p.pos
	if p.atEnd() || !isIdentifierStart(p.src[p.pos]) {
		return "", p.errorAt(p.pos, "the "+what+" must be a plain name like users, made of letters, digits and underscores")
	}
	for !p.atEnd() && isIdentifierChar(p.src[p.pos]) {
		p.pos++
	}

	name := p.src[start:p.pos]
	if len(name) > maxIdentifier {
		return "", p.errorAt(start, "the "+what+" name is too long")
	}
	return name, nil
}

// word reads one of the three keywords the grammar has, and refuses everything
// else that looks like one. Everything else is JavaScript: a variable, a
// constructor, a function. The named ones get told what to write instead;
// nothing that arrives here is echoed back, since it is the caller's own text.
func (p *parser) word() (Value, error) {
	start := p.pos
	switch word := p.scanWord(); word {
	case "true":
		return Value{kind: KindBool, boolean: true}, nil
	case "false":
		return Value{kind: KindBool}, nil
	case "null":
		return Value{kind: KindNull}, nil
	default:
		if instead, named := insteadOf[word]; named {
			return Value{}, p.errorAt(start, word+"(...) is JavaScript, and go-db's MongoDB grammar is JSON: write "+instead+" instead")
		}
		return Value{}, p.errorAt(start, "go-db's MongoDB grammar has no variables, constructors or expressions, only JSON values")
	}
}

// insteadOf answers the JavaScript constructors a mongosh user reaches for
// first with the extended JSON that means the same thing. MongoDB's own drivers
// read these object forms, so the answer is not a workaround — it is the
// interchange form, and this grammar passes it through as the plain object it
// is.
var insteadOf = map[string]string{
	"ObjectId":      `{"$oid": "…"}`,
	"ISODate":       `{"$date": "…"}`,
	"Date":          `{"$date": "…"}`,
	"NumberLong":    `{"$numberLong": "…"}`,
	"NumberInt":     `{"$numberInt": "…"}`,
	"NumberDecimal": `{"$numberDecimal": "…"}`,
	"BinData":       `{"$binary": {"base64": "…", "subType": "…"}}`,
	"Timestamp":     `{"$timestamp": {"t": 0, "i": 0}}`,
	"MinKey":        `{"$minKey": 1}`,
	"MaxKey":        `{"$maxKey": 1}`,
}

// number reads a JSON number. The grammar is JSON's exactly — no leading plus,
// no leading zero, no bare leading or trailing dot, no hexadecimal — so that
// what was accepted can be handed to the driver as written.
func (p *parser) number() (Value, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}

	switch {
	case p.atEnd() || !isDigit(p.src[p.pos]):
		return Value{}, p.numberError(start)
	case p.src[p.pos] == '0':
		p.pos++
	default:
		p.skipDigits()
	}

	if p.peek() == '.' {
		p.pos++
		if p.atEnd() || !isDigit(p.src[p.pos]) {
			return Value{}, p.numberError(start)
		}
		p.skipDigits()
	}

	if c := p.peek(); c == 'e' || c == 'E' {
		p.pos++
		if c := p.peek(); c == '+' || c == '-' {
			p.pos++
		}
		if p.atEnd() || !isDigit(p.src[p.pos]) {
			return Value{}, p.numberError(start)
		}
		p.skipDigits()
	}

	// 01, 1.2.3 and 0x1f all end a valid number and then carry on with
	// something number-shaped. Catching them here says "that is not a number"
	// rather than "a comma was expected", which is what the human needs to
	// read.
	if !p.atEnd() && (isWordChar(p.src[p.pos]) || p.src[p.pos] == '.') {
		return Value{}, p.numberError(start)
	}
	return Value{kind: KindNumber, text: p.src[start:p.pos]}, nil
}

func (p *parser) numberError(start int) error {
	return p.errorAt(start, "that is not a number go-db can read: write numbers as JSON does, like 12, -3.5 or 1e3")
}

// text reads a quoted string, in either quote, and returns it decoded. The
// closing quote is the only thing that ends it: a call written inside a string
// is text, and a bracket inside one closes nothing.
func (p *parser) text() (Value, error) {
	quote := p.src[p.pos]
	p.pos++

	var out strings.Builder
	for {
		if p.atEnd() {
			return Value{}, p.errorAt(p.pos, "the string is not closed")
		}

		switch c := p.src[p.pos]; {
		case c == quote:
			p.pos++
			return Value{kind: KindString, text: out.String()}, nil

		case c == '\\':
			if err := p.escape(&out); err != nil {
				return Value{}, err
			}

		case c < 0x20:
			return Value{}, p.errorAt(p.pos, `a string cannot hold a raw newline or tab: write \n or \t`)

		default:
			out.WriteByte(c)
			p.pos++
		}
	}
}

// escape decodes one backslash escape into out, the backslash unread.
func (p *parser) escape(out *strings.Builder) error {
	start := p.pos
	p.pos++ // '\'
	if p.atEnd() {
		return p.errorAt(p.pos, "the string is not closed")
	}

	c := p.src[p.pos]
	p.pos++
	switch c {
	case '"', '\'', '\\', '/':
		out.WriteByte(c)
	case 'b':
		out.WriteByte('\b')
	case 'f':
		out.WriteByte('\f')
	case 'n':
		out.WriteByte('\n')
	case 'r':
		out.WriteByte('\r')
	case 't':
		out.WriteByte('\t')
	case 'u':
		return p.unicodeEscape(out, start)
	default:
		return p.errorAt(start, `that is not an escape go-db knows: \" \' \\ \/ \b \f \n \r \t and \uXXXX are all of them`)
	}
	return nil
}

// unicodeEscape decodes \uXXXX, and the surrogate pair that spells a character
// outside the basic plane, with "u" already consumed.
func (p *parser) unicodeEscape(out *strings.Builder, start int) error {
	badEscape := func() error {
		return p.errorAt(start, `a \u escape must spell a whole character, in four hexadecimal digits`)
	}

	first, ok := p.hex4()
	if !ok {
		return badEscape()
	}

	switch {
	case utf16.IsSurrogate(rune(first)):
		if p.pos+1 >= len(p.src) || p.src[p.pos] != '\\' || p.src[p.pos+1] != 'u' {
			return badEscape()
		}
		p.pos += 2

		second, ok := p.hex4()
		if !ok {
			return badEscape()
		}
		paired := utf16.DecodeRune(rune(first), rune(second))
		if paired == utf8.RuneError {
			return badEscape()
		}
		out.WriteRune(paired)

	default:
		out.WriteRune(rune(first))
	}
	return nil
}

// hex4 reads four hexadecimal digits as one code unit.
func (p *parser) hex4() (uint32, bool) {
	if p.pos+4 > len(p.src) {
		return 0, false
	}

	var value uint32
	for i := 0; i < 4; i++ {
		c := p.src[p.pos+i]
		switch {
		case c >= '0' && c <= '9':
			value = value<<4 | uint32(c-'0')
		case c >= 'a' && c <= 'f':
			value = value<<4 | uint32(c-'a'+10)
		case c >= 'A' && c <= 'F':
			value = value<<4 | uint32(c-'A'+10)
		default:
			return 0, false
		}
	}
	p.pos += 4
	return value, true
}

// descend opens one level of nesting, or refuses to.
func (p *parser) descend() error {
	if p.depth >= maxDepth {
		return p.errorAt(p.pos, "this value is nested too deeply for go-db to read")
	}
	p.depth++
	return nil
}

// scanWord reads a run of word characters, which is how both a keyword and a
// name are recognised before anything is decided about it. It returns "" when
// the next character does not start one.
func (p *parser) scanWord() string {
	start := p.pos
	for !p.atEnd() && isWordChar(p.src[p.pos]) {
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) skipDigits() {
	for !p.atEnd() && isDigit(p.src[p.pos]) {
		p.pos++
	}
}

func (p *parser) skipSpace() {
	for !p.atEnd() {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			p.pos++
		default:
			return
		}
	}
}

// peek is the next byte, or -1 at the end of the statement. It is an int so
// that the end is a value no byte can be mistaken for.
func (p *parser) peek() int {
	if p.atEnd() {
		return -1
	}
	return int(p.src[p.pos])
}

func (p *parser) atEnd() bool { return p.pos >= len(p.src) }

// errorAt builds the refusal, with the position counted in characters because
// that is what a human counting through their own statement counts.
func (p *parser) errorAt(offset int, message string) error {
	if offset > len(p.src) {
		offset = len(p.src)
	}
	return fmt.Errorf("%s (at character %d)", message, utf8.RuneCountInString(p.src[:offset])+1)
}

// describe names the character that stopped the parser, and declines to when
// naming it would mean putting arbitrary bytes into a line the editor and the
// audit log will show.
func describe(c int) string {
	if c >= 0x21 && c <= 0x7e {
		return "'" + string(rune(c)) + "'"
	}
	return "a character that cannot start one"
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isIdentifierStart(c byte) bool { return isLetter(c) || c == '_' }

func isIdentifierChar(c byte) bool { return isIdentifierStart(c) || isDigit(c) }

// isWordChar covers a field name and a keyword, which may hold the dollar an
// identifier may not.
func isWordChar(c byte) bool { return isIdentifierChar(c) || c == '$' }
