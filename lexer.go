package unpeu

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/textproto"
	"strconv"
	"strings"
)

// lexer is responsible for reading input, and making sense of it
type lexer struct {
	// Line based reader
	reader *textproto.Reader
	// The current line
	line []byte
	// The index to the current character
	idx int
	// The start of tokens, used for rewinding to the previous token
	tokens []int
	// If true, the line has been entirely been consumed
	done bool
}

// Ascii codes
const (
	endOfInput       = 0x00
	cr               = 0x0d
	lf               = 0x0a
	space            = 0x20
	doubleQuote      = 0x22
	plus             = 0x2b
	zero             = 0x30
	nine             = 0x39
	leftCurly        = 0x7b
	rightCurly       = 0x7d
	leftParenthesis  = 0x28
	rightParenthesis = 0x29
	leftBracket      = 0x5b
	rightBracket     = 0x5d
	percent          = 0x25
	asterisk         = 0x2a
	backslash        = 0x5c
	lessThan         = 0x3c
	moreThan         = 0x3e
	dot              = 0x2e
)

// astringExceptionsChar is a list of chars that are not present in the astring charset
var astringExceptionsChar = []byte{
	space,
	leftParenthesis,
	rightParenthesis,
	percent,
	asterisk,
	backslash,
	leftCurly,
}

// tagExceptionsChar is a list of chars that are not present in the tag charset
var tagExceptionsChar = []byte{
	space,
	leftParenthesis,
	rightParenthesis,
	percent,
	asterisk,
	backslash,
	leftCurly,
	plus,
}

// listMailboxExceptionsChar is a list of chars that are  not present in the list-mailbox charset
var listMailboxExceptionsChar = []byte{
	space,
	leftParenthesis,
	rightParenthesis,
	rightBracket,
	backslash,
	leftCurly,
}

// searchStringExceptionsChar is a list of chars that delimit the
// parsing of search string tokens
var searchStringExceptionsChar = []byte{
	space,
	leftParenthesis,
	rightParenthesis,
	percent,
	backslash,
	leftCurly,
}

// createLexer creates a partially initialised IMAP lexer
// lexer.newLine() must be the first call to this lexer
func createLexer(in *bufio.Reader) *lexer {
	return &lexer{reader: textproto.NewReader(in)}
}

//-------- IMAP tokens ---------------------------------------------------------

// astring treats the input as a string
func (l *lexer) astring() (bool, string) {
	l.skipSpace()
	l.startToken()

	return l.generalString("ASTRING", astringExceptionsChar)
}

func (l *lexer) searchString() (bool, string) {
	l.skipSpace()
	l.startToken()

	c := l.current()
	switch c {
	case leftParenthesis:
		l.consume()
		return true, string(leftParenthesis)
	case rightParenthesis:
		l.consume()
		return true, string(rightParenthesis)
	}
	return l.generalString("SEARCH STRING", searchStringExceptionsChar)
}

// tag treats the input as a tag string
func (l *lexer) tag() (bool, string) {
	l.skipSpace()
	l.startToken()

	return l.nonquoted("TAG", tagExceptionsChar)
}

// listMailbox treats the input as a list mailbox
func (l *lexer) listMailbox() (bool, string) {
	l.skipSpace()
	l.startToken()

	return l.generalString("LIST-MAILBOX", listMailboxExceptionsChar)
}

// An element is a single cell in a list as defined by RFC 3501 4.4: it
// can either be a string value OR be a list of other elements
type element struct {
	stringValue string
	children    []element
}

// listStrings returns a list of elements that have been parsed from a
// list as defined by RFC 3501 4.4. It returns true if the input string
// is correct, along with the (possibly nested) elements
func (l *lexer) listStrings() (bool, []element) {
	l.skipSpace()
	l.startToken()

	elements := make([]element, 0)
	var e element

	b := l.current()
	if b != leftParenthesis {
		return false, nil
	}
	l.consume()

read:
	for {
		b := l.current()
		switch b {
		case lf:
			// We should never reach lf, parsing should end naturally at the
			// last ')'
			return false, nil
		case leftParenthesis:
			ok, children := l.listStrings()
			if !ok {
				return false, nil
			}
			e.children = children
			elements = append(elements, e)
			e = element{}
		case space:
			elements = append(elements, e)
			e = element{}
		case rightParenthesis:
			elements = append(elements, e)
			break read
		default:
			e.stringValue += string(b)
		}
		l.consume()
	}

	// Discard last ')'
	l.consume()
	return true, elements
}

type fetchArgument struct {
	text    string
	section string

	// Only if text == "HEADER.FIELDS" or text == "HEADER.FIELDS.NOT"
	fields []string
	part   []int

	// if -1 it means its value wasn't specified. This is needed as the
	// response will be different
	offset int
	length int
}

func (l *lexer) fetchArguments() (sequenceSet string, args []fetchArgument, err error) {
	l.skipSpace()
	l.startToken()

	var ok bool
	ok, sequenceSet = l.nonquoted("SEQUENCE SET", []byte{space})
	if !ok {
		return sequenceSet, args, fmt.Errorf("No sequence set")
	}
	if !isValid(sequenceSet) {
		return sequenceSet, args, fmt.Errorf("No sequence set")
	}

	args = make([]fetchArgument, 0)
	var hasList bool
	var numFields int

accum:
	for {
		l.skipSpace()
		switch l.current() {
		case leftParenthesis:
			hasList = true
			l.consume()
			continue
		case rightParenthesis, lf:
			break accum
		}

		ok, next := l.nonquoted("FETCH-ATT", []byte{leftBracket, rightParenthesis})
		if !ok {
			return sequenceSet, args, fmt.Errorf("Error getting next fetch-att")
		}
		numFields++
		// At this point current points to the char after next
		switch next {
		case "ENVELOPE", "FLAGS", "INTERNALDATE",
			"RFC822", "RFC822.HEADER", "RFC822.SIZE", "RFC822.TEXT",
			"BODYSTRUCTURE", "UID":
			args = append(args, fetchArgument{text: next})
		case "ALL":
			args = append(args, fetchArgument{text: "FLAGS"})
			args = append(args, fetchArgument{text: "INTERNALDATE"})
			args = append(args, fetchArgument{text: "RFC822.SIZE"})
			args = append(args, fetchArgument{text: "ENVELOPE"})
		case "FAST":
			args = append(args, fetchArgument{text: "FLAGS"})
			args = append(args, fetchArgument{text: "INTERNALDATE"})
			args = append(args, fetchArgument{text: "RFC822.SIZE"})
		case "FULL":
			args = append(args, fetchArgument{text: "FLAGS"})
			args = append(args, fetchArgument{text: "INTERNALDATE"})
			args = append(args, fetchArgument{text: "RFC822.SIZE"})
			args = append(args, fetchArgument{text: "ENVELOPE"})
			args = append(args, fetchArgument{text: "BODY"})
		case "BODY", "BODY.PEEK":
			c := l.current()
			if c == space {
				if next == "BODY" {
					args = append(args, fetchArgument{text: next})
					continue
				} else {
					return sequenceSet, args, fmt.Errorf("Unexpected space after " + next)
				}
			}
			if c != leftBracket {
				return sequenceSet, args, fmt.Errorf("Expected '[' after " + next + ", got " + string(c))
			}
			ok, section := l.sectionArgs()
			if !ok {
				return sequenceSet, args, fmt.Errorf("Couldn't extract section")
			}
			section.text = next
			args = append(args, section)
		default:
			return sequenceSet, args, fmt.Errorf("Unknown section-text: %q\n", next)
		}
	}
	if !hasList && numFields > 1 {
		return sequenceSet, args, fmt.Errorf("Multiple arguments without parenthesis")
	}
	return sequenceSet, args, nil
}

var knownBodySections = map[string]struct{}{
	"":                  struct{}{},
	"HEADER":            struct{}{},
	"HEADER.FIELDS":     struct{}{},
	"HEADER.FIELDS.NOT": struct{}{},
	"TEXT":              struct{}{},
	"MIME":              struct{}{},
}

func (l *lexer) sectionArgs() (bool, fetchArgument) {
	s := fetchArgument{
		fields: make([]string, 0),
	}

	// Elide '['
	l.consume()

	// Extract section-part
	var sectionPartString string
	for {
		c := l.current()
		if c > '0' && c < '9' || c == dot {
			sectionPartString += string(c)
		} else {
			break
		}
		l.consume()
	}

	if len(sectionPartString) > 0 {
		// Elide last dot
		if sectionPartString[len(sectionPartString)-1] == dot {
			sectionPartString = sectionPartString[:len(sectionPartString)-1]
		}
		split := strings.Split(sectionPartString, string(dot))
		s.part = make([]int, 0, len(split))
		for _, ss := range split {
			asInt, err := strconv.Atoi(ss)
			if err != nil {
				//log.Printf("Invalid section-part: %q\n", sectionPartString)
				return false, s
			}
			s.part = append(s.part, asInt)
		}
	}

	// Extract section-text
	// HACK: we can have an empty section text, which is recognized as
	// wrong by nonquoted
	ok, sectionName := l.nonquoted("SECTION-TEXT", []byte{space, rightBracket})
	if !ok && l.current() != ']' {
		//log.Println("Invalid section-text")
		return false, s
	}
	_, ok = knownBodySections[sectionName]
	if !ok {
		//log.Printf("Unknown section-text: %q\n", sectionName)
		return false, s
	}
	if sectionName == "MIME" && len(s.part) == 0 {
		//log.Println("Invalid MIME at top-level")
		return false, s
	}
	s.section = sectionName

	l.skipSpace()

	// Extract fields identifier, if they exist
	if l.current() == leftParenthesis {
		l.consume()
		for {
			l.skipSpace()
			c := l.current()
			if c == rightParenthesis {
				break
			}
			ok, field := l.astring()
			if !ok {
				//log.Println("Invalild field")
				return false, s
			}
			s.fields = append(s.fields, field)
		}
		l.consume()
	}

	if len(s.fields) > 0 &&
		s.section != "HEADER.FIELDS" && s.section != "HEADER.FIELDS.NOT" {
		//log.Printf("Unexpected fields with text being %q\n", s.section)
		return false, s
	}

	if len(s.fields) == 0 &&
		(s.section == "HEADER.FIELDS" || s.section == "HEADER.FIELDS.NOT") {
		//log.Printf("Missing fields for %q\n", s.section)
		return false, s
	}

	// Elide ']'
	l.consume()

	// Extract offset, if it exists
	s.offset = -1
	if c := l.current(); c == lessThan {
		l.consume()
		ok, offset := l.nonquoted("NUMBER", []byte{dot, moreThan})
		if !ok {
			return false, s
		}
		var err error
		s.offset, err = strconv.Atoi(offset)
		if err != nil {
			//log.Printf("Expected number as offset, got %q\n", offset)
			return false, s
		}

		// Skip dot
		c := l.current()
		if c == moreThan {
			l.consume()
			return true, s
		}
		if c != dot {
			//log.Printf("Expected dot as offset and length separater, got %q\n", c)
			return false, s
		}
		l.consume()

		// Extract length
		ok, length := l.nonquoted("NZ-NUMBER", []byte{moreThan})
		if !ok {
			return false, s
		}
		s.length, err = strconv.Atoi(length)
		if err != nil {
			//log.Printf("Expected number as length, got %q\n", length)
			return false, s
		}
		if s.length == 0 {
			//log.Println("length should be >0")
			return false, s
		}
		l.consume()
	}

	return true, s
}

//-------- IMAP token helper functions -----------------------------------------

// generalString handles a string that can be bare, a literal or quoted
func (l *lexer) generalString(name string, exceptions []byte) (bool, string) {

	var str string
	var ok bool
	var err error

	// Consider the first character - this gives the type of argument
	switch l.current() {
	case doubleQuote:
		l.consume()
		str, err = l.qstring()
		ok = err == nil
	case leftCurly:
		l.consume()
		str, err = l.literal()
		ok = err == nil
	default:
		ok, str = l.nonquoted(name, exceptions)
	}

	if err != nil {
		log.Println("Error in generalString:", err)
	}
	return ok, str
}

// qstring reads a quoted string
func (l *lexer) qstring() (string, error) {

	var buffer = make([]byte, 0, 16)

	c := l.current()

	// Collect the characters that are within double quotes
	for c != doubleQuote {

		switch c {
		case cr, lf:
			err := parseError(fmt.Sprintf(
				"Unexpected character %q in quoted string", c))
			return "", err
		case backslash:
			c = l.consume()
			buffer = append(buffer, c)
		default:
			buffer = append(buffer, c)
		}

		// Get the next byte
		c = l.consume()
	}

	// Ignore the closing quote
	l.consume()

	return string(buffer), nil
}

// literal parses a length tagged literal
// TODO: send a continuation request after the first line is read
func (l *lexer) literal() (string, error) {
	length, err := l.literalLength()
	if err != nil {
		return "", err
	}

	return l.literalRest(length)
}

// literalLength retrieves the length of the following literal. It stops
// after the closing curly bracket ('}'); after literalLength you can
// directly read the literal value through literalRest
func (l *lexer) literalLength() (int64, error) {

	lengthBuffer := make([]byte, 0, 8)

	c := l.current()

	// Get the length of the literal
	for c != rightCurly {
		if c < zero || c > nine {
			err := parseError(fmt.Sprintf(
				"Unexpected character %q in literal length", c))
			return 0, err
		}

		lengthBuffer = append(lengthBuffer, c)
		c = l.consume()
	}

	// Consume one more so the rest can start at the rest of the content
	l.consume()

	// Extract the literal length as an int
	length, err := strconv.ParseInt(string(lengthBuffer), 10, 32)
	if err != nil {
		return 0, parseError(err.Error())
	}

	// Does the literal have a valid length?
	if length <= 0 {
		return 0, fmt.Errorf("Invalid length: %d", length)
	}
	return length, nil
}

func (l *lexer) literalRest(length int64) (string, error) {
	out := make([]byte, length)
	fill := out

	// First, drain current line
	for c := l.current(); c != lf; c = l.consume() {
		fill[0] = c
		fill = fill[1:]
	}

	// Second, read directly from buffered reader
	n, err := io.ReadFull(l.reader.R, fill)
	if n != len(fill) {
		err = fmt.Errorf("Short read: got %d, expected %d", n, len(fill))
	}
	// Reinstall the lexer with the bufio Reader in its current state
	l.reader = textproto.NewReader(l.reader.R)
	l.line = nil
	l.idx = 0
	l.tokens = nil
	l.newLine()

	return string(out), err
}

// nonquoted reads a non-quoted string
func (l *lexer) nonquoted(name string, exceptions []byte) (bool, string) {

	buffer := make([]byte, 0, 16)

	// Get the current byte
	c := l.current()

	for c > space && c < 0x7f && -1 == bytes.IndexByte(exceptions, c) {

		buffer = append(buffer, c)
		c = l.consume()
	}

	// Check that characters were consumed
	if len(buffer) == 0 {
		return false, ""
	}

	return true, string(buffer)
}

//-------- Low level lexer functions -------------------------------------------

// consume a single byte and return the new character
// Does not go through newlines
func (l *lexer) consume() byte {

	// Is there any line left?
	if l.idx >= len(l.line)-1 {
		l.done = true
		// Return linefeed
		return lf
	}

	// Move to the next byte
	l.idx += 1
	return l.current()
}

// current gets the current byte
func (l *lexer) current() byte {
	if l.done {
		return lf
	}
	return l.line[l.idx]
}

// newLine moves onto a new line
func (l *lexer) newLine() error {

	// Read the line
	line, err := l.reader.ReadLineBytes()
	if err != nil && err != io.EOF {
		log.Println("Error at newline:", err)
		return parseError(err.Error())
	}
	if len(line) == 0 {
		return io.EOF
	}

	// Reset the lexer - we cannot rewind past line boundaries
	l.line = line
	l.idx = 0
	l.tokens = make([]int, 0, 8)
	l.done = false
	return nil
}

// skipSpace skips any spaces
func (l *lexer) skipSpace() {
	c := l.current()

	for c == space {
		c = l.consume()
	}
}

// startToken marks the start a new token
func (l *lexer) startToken() {
	l.tokens = append(l.tokens, l.idx)
}

// pushBack moves back one token
func (l *lexer) pushBack() {
	last := len(l.tokens) - 1
	l.idx = l.tokens[last]
	l.tokens = l.tokens[:last]
}
