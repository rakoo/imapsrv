package unpeu

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/textproto"
	"strconv"
	"time"
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
	rightBracket     = 0x5d
	percent          = 0x25
	asterisk         = 0x2a
	backslash        = 0x5c
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

type searchArgument struct {
	not      bool
	key      string
	values   []string
	children []searchArgument
	or       bool
}

func aggregateSearchArguments(fullLine []byte) ([]searchArgument, error) {

	l := &lexer{
		reader: textproto.NewReader(bufio.NewReader(bytes.NewReader(fullLine))),
	}
	l.newLine()

	args := make([]searchArgument, 0)
	previousArgs := args

	depth := 0
	var currentArg searchArgument

	// When an OR is met, we use these to control how the following
	// arguments are handled
	var isInOr bool
	var orCount int

	for {
		l.skipSpace()

		if l.current() == lf {
			// Then exit if we're at the end of the line
			if depth != 0 {
				return nil, fmt.Errorf("Uneven parentheses")
			}
			return args, nil
		}

		ok, next := l.searchString()
		if !ok {
			log.Println(l.idx, string(l.line))
			return nil, fmt.Errorf("Couldn't parse arguments")
		}

		switch next {
		case "(":
			// Push current argument being built
			currentArg.children = make([]searchArgument, 0)
			args = append(args, currentArg)

			previousArgs = args
			args = currentArg.children

			depth++
		case ")":
			// Get back the parent list of arguments, and push the argument
			// we've been building to its end
			args = previousArgs
			depth--
		case
			"ALL", "ANSWERED", "DELETED", "FLAGGED", "NEW", "OLD",
			"RECENT", "SEEN", "UNANSWERED", "UNDELETED", "UNFLAGGED",
			"UNSEEN", "DRAFT", "UNDRAFT":

			currentArg.key = next
			args = append(args, currentArg)
			currentArg = searchArgument{}
		case "KEYWORD", "UNKEYWORD":
			fallthrough
		case "BCC", "BODY", "CC", "FROM", "SUBJECT", "TEXT", "TO":
			currentArg.key = next

			ok, value := l.astring()
			if !ok {
				return nil, fmt.Errorf("Couldn't parse argument to %s", next)
			}

			currentArg.values = []string{value}
			args = append(args, currentArg)
			currentArg = searchArgument{}
		case "BEFORE", "ON", "SINCE", "SENTBEFORE", "SENTON", "SENTSINCE":
			currentArg.key = next

			ok, value := l.astring()
			if !ok {
				return nil, fmt.Errorf("Couldn't parse argument to %s", next)
			}
			// Make sure it's a valid date, even though we don't care about
			// the exact date (for now just keep the string)
			_, err := time.Parse("02-Jan-2006", value)
			if err != nil {
				return nil, err
			}

			currentArg.values = []string{value}
			args = append(args, currentArg)
			currentArg = searchArgument{}
		case "LARGER", "SMALLER":
			currentArg.key = next

			ok, value := l.astring()
			if !ok {
				return nil, fmt.Errorf("Couldn't parse argument to %s", next)
			}
			// Make sure it's a valid number, even though we don't care about
			// the exact date (for now just keep the string)
			_, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}

			currentArg.values = []string{value}
			args = append(args, currentArg)
			currentArg = searchArgument{}

		case "HEADER":
			currentArg.key = next

			ok, headerField := l.astring()
			if !ok {
				return nil, fmt.Errorf("Couldn't parse header field for HEADER")
			}
			ok, headerValue := l.astring()
			if !ok {
				return nil, fmt.Errorf("Couldn't parse header value for HEADER")
			}

			currentArg.values = []string{headerField, headerValue}
			args = append(args, currentArg)
			currentArg = searchArgument{}
		case "NOT":
			currentArg.not = true
		case "OR":
			isInOr = true
			orCount = 3 // We're going to decrement just after that
			currentArg.or = true
			currentArg.children = make([]searchArgument, 0, 2)
			args = append(args, currentArg)
			previousArgs = args
			args = currentArg.children
		case "UID":
			currentArg.key = next
			ok, sequenceSet := l.astring()
			if !ok {
				return nil, fmt.Errorf("Couldn't parse sequence set to UID")
			}
			if !isValid(sequenceSet) {
				return nil, fmt.Errorf("Couldn't parse sequence set to UID")
			}

			currentArg.values = []string{sequenceSet}
			args = append(args, currentArg)
			currentArg = searchArgument{}

		default:
			if isValid(next) {
				currentArg.key = "SEQUENCESET" // Fake key for more consistency
				currentArg.values = []string{next}
				args = append(args, currentArg)
				currentArg = searchArgument{}
			} else {
				return nil, fmt.Errorf("Unrecognized search argument: %s", next)
			}
		}

		orCount--
		if isInOr && orCount == 0 {
			args = previousArgs
			isInOr = false
		}
	}
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
