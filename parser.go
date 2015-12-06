package unpeu

import (
	"bufio"
	"fmt"
	"strings"
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
	p.lexer.newLine()

	// Expect a tag followed by a command
	tagAndComm, err := p.expectString(p.lexer.tag, p.lexer.astring)
	if err != nil {
		return nil, err
	}
	tag := tagAndComm[0]
	rawCommand := tagAndComm[1]

	// Parse the command based on its lowercase value
	lcCommand := strings.ToLower(rawCommand)

	switch lcCommand {
	case "noop":
		return p.noop(tag), nil
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
	default:
		return p.unknown(tag, rawCommand), nil
	}
}

// noop creates a NOOP command
func (p *parser) noop(tag string) command {
	return &noop{tag: tag}
}

// capability creates a CAPABILITY command
func (p *parser) capability(tag string) command {
	return &capability{tag: tag}
}

// login creates a LOGIN command
func (p *parser) login(tag string) (command, error) {

	// Get the command arguments
	userAndPassword, err := p.expectString(p.lexer.astring, p.lexer.astring)
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
	ret, err := p.expectString(p.lexer.astring)
	if err != nil {
		return nil, err
	}

	return &selectMailbox{tag: tag, mailbox: ret[0]}, nil
}

// statusCmd creates a status command
func (p *parser) statusCmd(tag string) (command, error) {

	// Get the mailbox name
	ret, err := p.expectString(p.lexer.astring)
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
	refAndMailbox, err := p.expectString(p.lexer.astring, p.lexer.listMailbox)
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

//----- Helper functions -------------------------------------------------------

// expectString gets one or more string token(s) using the given lexer
// function(s)
// If the lexing fails, then this will return a parse error
func (p *parser) expectString(lexes ...func() (bool, string)) ([]string, error) {
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
