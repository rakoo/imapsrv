package unpeu

import (
	"bufio"
	"fmt"
	"strings"
	"time"
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
		return p.search(tag, uidMod)
	case "fetch":
		return p.fetch(tag, uidMod)
	case "store":
		return p.store(tag, uidMod)
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

func (p *parser) search(tag string, returnUid bool) (command, error) {
	p.lexer.skipSpace()
	return &searchCmd{
		l:         p.lexer,
		tag:       tag,
		returnUid: returnUid,
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
