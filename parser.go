package unpeu

import (
	"bufio"
	"bytes"
	"fmt"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"code.google.com/p/go-charset/charset"
	_ "code.google.com/p/go-charset/data"
)

// parser can parse IMAP commands
type parser struct {
	lexer *lexer
}

// parseError is an Error from the IMAP parser or lexer
type parseError string

// Error returns the string representation of the parseError
func (e parseError) Error() string {
	return string(e)
}

// createParser creates a new IMAP parser, reading from the Reader
func createParser(in *bufio.Reader) *parser {
	lexer := createLexer(in)
	return &parser{lexer: lexer}
}

//----- Commands ---------------------------------------------------------------

// next attempts to read the next command
func (p *parser) next() (command, error) {

	// All commands start on a new line
	err := p.lexer.newLine()
	if err != nil {
		return nil, err
	}

	// Expect a tag followed by a command
	tagAndComm, err := p.expectStrings(p.lexer.tag, p.lexer.astring)
	if err != nil {
		return nil, err
	}
	tag := tagAndComm[0]
	rawCommand := tagAndComm[1]

	// Parse the command based on its lowercase value
	lcCommand := strings.ToLower(rawCommand)

	var uidMod bool
	if lcCommand == "uid" {
		uidMod = true
		realCommand, err := p.expectStrings(p.lexer.astring)
		if err != nil {
			return nil, err
		}
		lcCommand = strings.ToLower(realCommand[0])
	}

	switch lcCommand {
	case "noop":
		return p.noop(tag), nil
	case "check":
		return p.check(tag), nil
	case "capability":
		return p.capability(tag), nil
	case "starttls":
		return p.starttls(tag), nil
	case "login":
		return p.login(tag)
	case "logout":
		return p.logout(tag), nil
	case "select":
		return p.selectCmd(tag)
	case "status":
		return p.statusCmd(tag)
	case "list":
		return p.list(tag)
	case "append":
		return p.append(tag)
	case "search":
		return p.search(tag, uidMod, false)
	case "fetch":
		return p.fetch(tag, uidMod)
	case "store":
		return p.store(tag, uidMod)
	case "thread":
		return p.search(tag, uidMod, true)
	default:
		return p.unknown(tag, rawCommand), nil
	}
}

// noop creates a NOOP command
func (p *parser) noop(tag string) command {
	return &noop{tag: tag}
}

// check creates a CHECK command
func (p *parser) check(tag string) command {
	return &check{tag: tag}
}

// capability creates a CAPABILITY command
func (p *parser) capability(tag string) command {
	return &capability{tag: tag}
}

// login creates a LOGIN command
func (p *parser) login(tag string) (command, error) {

	// Get the command arguments
	userAndPassword, err := p.expectStrings(p.lexer.astring, p.lexer.astring)
	if err != nil {
		return nil, err
	}
	userId := userAndPassword[0]
	password := userAndPassword[1]

	// Create the command
	return &login{tag: tag, userId: userId, password: password}, nil
}

// starttls creates a starttls command
func (p *parser) starttls(tag string) command {
	return &starttls{tag: tag}
}

// logout creates a LOGOUT command
func (p *parser) logout(tag string) command {
	return &logout{tag: tag}
}

// selectCmd creates a select command
func (p *parser) selectCmd(tag string) (command, error) {

	// Get the mailbox name
	ret, err := p.expectStrings(p.lexer.astring)
	if err != nil {
		return nil, err
	}

	return &selectMailbox{tag: tag, mailbox: ret[0]}, nil
}

// statusCmd creates a status command
func (p *parser) statusCmd(tag string) (command, error) {

	// Get the mailbox name
	ret, err := p.expectStrings(p.lexer.astring)
	if err != nil {
		return nil, err
	}

	ok, elements := p.lexer.listStrings()
	if !ok {
		return nil, parseError("Invalid list of elements")
	}
	params := make([]string, len(elements))
	for i, e := range elements {
		params[i] = e.stringValue
	}

	return &statusMailbox{tag: tag, mailbox: ret[0], params: params}, nil
}

// list creates a LIST command
func (p *parser) list(tag string) (command, error) {

	// Get the command arguments
	refAndMailbox, err := p.expectStrings(p.lexer.astring, p.lexer.listMailbox)
	if err != nil {
		return nil, err
	}
	reference := refAndMailbox[0]
	if strings.EqualFold(reference, "inbox") {
		reference = "INBOX"
	}
	mailbox := refAndMailbox[1]

	return &list{tag: tag, reference: reference, mboxPattern: mailbox}, nil
}

// unknown creates a placeholder for an unknown command
func (p *parser) unknown(tag string, cmd string) command {
	return &unknown{tag: tag, cmd: cmd}
}

// append creates the APPEND command
func (p *parser) append(tag string) (command, error) {
	mailbox, err := p.expectStrings(p.lexer.astring)
	if err != nil {
		return nil, err
	}

	ac := &appendCmd{
		l:       p.lexer,
		tag:     tag,
		mailbox: mailbox[0],
	}

opts:
	for {
		// Optional arguments; we have to do it manually here
		p.lexer.skipSpace()
		p.lexer.startToken()

		c := p.lexer.current()
		switch c {
		case leftParenthesis:
			ok, flagElems := p.lexer.listStrings()
			if !ok {
				return nil, fmt.Errorf("Invalid flag list")
			}
			ac.flags = make([]string, len(flagElems))
			for i, fe := range flagElems {
				ac.flags[i] = fe.stringValue
			}
		case doubleQuote:
			p.lexer.consume()
			dateTime, err := p.lexer.qstring()
			if err != nil {
				return nil, err
			}
			ac.dateTime, err = time.Parse("02-Jan-2006 15:04:05 -0700", dateTime)
			if err != nil {
				return nil, err
			}
		case leftCurly:
			p.lexer.consume()
			ac.messageLength, err = p.lexer.literalLength()
			if err != nil {
				return nil, err
			}
			break opts
		default:
			return nil, fmt.Errorf("Parser unexpected %q", c)
		}
	}

	return ac, nil
}

func (p *parser) search(tag string, returnUid bool, returnThreads bool) (command, error) {
	p.lexer.skipSpace()
	return &searchCmd{
		l:             p.lexer,
		tag:           tag,
		returnUid:     returnUid,
		returnThreads: returnThreads,
	}, nil
}

func (p *parser) fetch(tag string, useUids bool) (command, error) {
	cmd := &fetchCmd{
		tag:     tag,
		useUids: useUids,
	}

	var err error
	cmd.sequenceSet, cmd.args, err = p.lexer.fetchArguments()
	return cmd, err
}

func (p *parser) store(tag string, useUids bool) (command, error) {
	p.lexer.skipSpace()

	// Sequence set
	ok, sequenceSet := p.lexer.nonquoted("SEQUENCE SET", []byte{space})
	if !ok {
		return nil, fmt.Errorf("No sequence set")
	}
	if !isValid(sequenceSet) {
		return nil, fmt.Errorf("No sequence set")
	}

	p.lexer.skipSpace()

	// Mode
	ok, itemName := p.lexer.astring()
	if !ok {
		return nil, fmt.Errorf("Invalid item name")
	}

	p.lexer.skipSpace()

	// Flags
	ok, flagElements := p.lexer.listStrings()
	if !ok {
		return nil, fmt.Errorf("No flags")
	}
	flags := make([]string, len(flagElements))
	for i, flagElem := range flagElements {
		flags[i] = flagElem.stringValue
	}

	return &storeCmd{
		itemName:    itemName,
		sequenceSet: sequenceSet,
		useUids:     useUids,
		flags:       flags,
		tag:         tag,
	}, nil
}

//----- Helper functions -------------------------------------------------------

// expectStrings gets one or more string token(s) using the given lexer
// function(s)
// If the lexing fails, then this will return a parse error
func (p *parser) expectStrings(lexes ...func() (bool, string)) ([]string, error) {
	strings := make([]string, len(lexes))

	for i, lex := range lexes {
		ok, ret := lex()
		if !ok {
			msg := fmt.Sprintf("Parser unexpected %q", p.lexer.current())
			err := parseError(msg)
			return strings, err
		}
		strings[i] = ret
	}

	return strings, nil
}

type searchArgument struct {
	not      bool
	key      string
	values   []string
	children []searchArgument
	or       bool

	// true if this argument is a list of other arguments inside
	// parenthesis
	group bool

	// used for aggregating
	depth int
}

func aggregateSearchArguments(fullLine []byte) ([]searchArgument, error) {

	l := &lexer{
		reader: textproto.NewReader(bufio.NewReader(bytes.NewReader(fullLine))),
	}
	l.newLine()

	args := make([]searchArgument, 0)

	depth := 0
	currentArg := searchArgument{
		depth:    depth,
		children: make([]searchArgument, 0),
	}

	var appendArg func(args []searchArgument, newArg searchArgument) (retArgs []searchArgument, currentArg searchArgument)
	appendArg = func(args []searchArgument, newArg searchArgument) (retArgs []searchArgument, currentArg searchArgument) {
		retArgs = append(args, newArg)
		currentArg = searchArgument{depth: depth, children: make([]searchArgument, 0)}
		return retArgs, currentArg
	}

	// By default everything is ASCII, which is correctly decoded by UTF-8
	translator, err := charset.TranslatorFrom("UTF-8")
	if err != nil {
		return nil, err
	}

	// When an OR is encountered, we use this to stack recursive ORs
	// Each level is initiated to 3, decremented once for each element
	// (including the OR). When we reach 0 it means the OR is done, so we
	// leave this sub-context by removing the count  from the stack.
	// Further OR may have been ongoing, so they keep on where they were
	// left at
	allOrs := make([]int, 0)

	// Big for loop. We read each argument from the next token. If we have
	// a real token (eg "ALL"), then we do a full loop; otherwise if it's
	// a token modifier (such as '(' or "OR") we interrupt the loop
	// (through continue) until we've read the full token
	for {
		l.skipSpace()

		if l.current() == lf {
			// Then exit if we're at the end of the line
			if depth != 0 {
				return nil, fmt.Errorf("Uneven parentheses")
			}
			break
		}

		ok, next := l.searchString()
		if !ok {
			return nil, fmt.Errorf("Couldn't parse arguments")
		}

		next = strings.ToUpper(next)

		switch next {
		case "(":
			currentArg.group = true
			args, currentArg = appendArg(args, currentArg)
			depth++
			currentArg.depth = depth
			continue
		case ")":
			depth--
		case
			"ALL", "ANSWERED", "DELETED", "FLAGGED", "NEW", "OLD",
			"RECENT", "SEEN", "UNANSWERED", "UNDELETED", "UNFLAGGED",
			"UNSEEN", "DRAFT", "UNDRAFT":

			currentArg.key = next
			args, currentArg = appendArg(args, currentArg)
		case "KEYWORD", "UNKEYWORD":
			fallthrough
		case "BCC", "BODY", "CC", "FROM", "SUBJECT", "TEXT", "TO":
			currentArg.key = next

			ok, value := l.astring()
			if !ok {
				return nil, fmt.Errorf("Couldn't parse argument to %s", next)
			}
			_, decoded, err := translator.Translate([]byte(value), true)
			if err != nil {
				return nil, fmt.Errorf("Invalid encoding (%s): %s", translator, err)
			}

			currentArg.values = []string{string(decoded)}
			args, currentArg = appendArg(args, currentArg)
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
			args, currentArg = appendArg(args, currentArg)
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
			args, currentArg = appendArg(args, currentArg)
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
			args, currentArg = appendArg(args, currentArg)
		case "NOT":
			currentArg.not = true
			continue
		case "OR":
			allOrs = append(allOrs, 2)
			currentArg.or = true
			currentArg.children = make([]searchArgument, 0, 2)
			args, currentArg = appendArg(args, currentArg)
			depth++
			currentArg.depth = depth
			continue
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
			args, currentArg = appendArg(args, currentArg)
		case "REFS":
			currentArg.key = next
			args, currentArg = appendArg(args, currentArg)
		default:
			if isValid(next) {
				currentArg.key = "SEQUENCESET" // Fake key for more consistency
				currentArg.values = []string{next}
				args, currentArg = appendArg(args, currentArg)
			} else {
				var foundCharset bool
				for _, knownCharset := range charset.Names() {
					if strings.ToLower(knownCharset) == strings.ToLower(next) {
						translator, err = charset.TranslatorFrom(next)
						if err != nil {
							return nil, fmt.Errorf("Invalid charset (%s): %s", next, err)
						}
						foundCharset = true
						break
					}
				}
				if foundCharset {
					continue
				}
				return nil, fmt.Errorf("Unrecognized search argument: %s", next)
			}
		}

		if len(allOrs) > 0 {
			allOrs[len(allOrs)-1]--
			if allOrs[len(allOrs)-1] == 0 {
				depth--
				currentArg.depth = depth
				allOrs = allOrs[:len(allOrs)-1]
			}
		}
	}

	// Inspired by camlistore
	// Take elements in reverse order. If its depth is lower than those
	// before, do nothing. If it's higher, it means it is actually a
	// parent of all those before; we remove those before from the new
	// list, and set them as children of the current one (not forgetting
	// to re-reverse order because they were inserted in reverse order)
	outReverse := make([]searchArgument, 0)
	for i := len(args) - 1; i >= 0; i-- {
		if len(outReverse) > 0 {
			sliceFrom := len(outReverse)
			for j := len(outReverse) - 1; j >= 0 && outReverse[j].depth > args[i].depth; j-- {
				sliceFrom--
			}
			for j := len(outReverse) - 1; j >= sliceFrom; j-- {
				args[i].children = append(args[i].children, outReverse[j])
			}
			outReverse = outReverse[:sliceFrom]
		}
		outReverse = append(outReverse, args[i])

	}
	out := make([]searchArgument, 0, len(outReverse))
	for i := len(outReverse) - 1; i >= 0; i-- {
		out = append(out, outReverse[i])
	}
	return out, nil
}
