package unpeu

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// command represents an IMAP command
type command interface {
	// Execute the command and return an IMAP response
	execute(s *session) *response
}

const (
	// pathDelimiter is the delimiter used to distinguish between different folders
	pathDelimiter = '/'
)

//------------------------------------------------------------------------------

// noop is a NOOP command
type noop struct {
	tag string
}

// execute a NOOP command
func (c *noop) execute(s *session) *response {
	return ok(c.tag, "NOOP Completed")
}

// check is a CHECK command
type check struct {
	tag string
}

// execute a CHECK command
func (c *check) execute(s *session) *response {
	return ok(c.tag, "CHECK Completed")
}

//------------------------------------------------------------------------------

// capability is a CAPABILITY command
type capability struct {
	tag string
}

// execute a capability
func (c *capability) execute(s *session) *response {
	var commands []string

	switch s.listener.encryption {
	case unencryptedLevel:
		// TODO: do we want to support this?

	case starttlsLevel:
		if s.encryption == tlsLevel {
			commands = append(commands, "AUTH=PLAIN")
		} else {
			commands = append(commands, "STARTTLS")
			commands = append(commands, "LOGINDISABLED")
		}

	case tlsLevel:
		commands = append(commands, "AUTH=PLAIN")
	}

	// Return all capabilities
	return ok(c.tag, "CAPABILITY completed").
		extra("CAPABILITY IMAP4rev1 " + strings.Join(commands, " "))
}

//------------------------------------------------------------------------------

type starttls struct {
	tag string
}

func (c *starttls) execute(sess *session) *response {
	sess.conn.Write([]byte(fmt.Sprintf("%s Begin TLS negotiation now", c.tag)))

	sess.conn = tls.Server(sess.conn, &tls.Config{Certificates: sess.listener.certificates})
	textConn := textproto.NewConn(sess.conn)

	sess.encryption = tlsLevel
	return empty().replaceBuffers(textConn)
}

//------------------------------------------------------------------------------

// login is a LOGIN command
type login struct {
	tag      string
	userId   string
	password string
}

// execute a LOGIN command
func (c *login) execute(sess *session) *response {

	// Has the user already logged in?
	if sess.st > notAuthenticated {
		message := "LOGIN already logged in"
		sess.log(message)
		return bad(c.tag, message)
	}

	auth, err := sess.server.config.authBackend.Authenticate(c.userId, c.password)

	if auth {
		sess.st = authenticated
		return ok(c.tag, "LOGIN completed")
	}
	log.Println("Login request:", auth, err)

	// Fail by default
	return no(c.tag, "LOGIN failure")
}

//------------------------------------------------------------------------------

// logout is a LOGOUT command
type logout struct {
	tag string
}

// execute a LOGOUT command
func (c *logout) execute(sess *session) *response {

	sess.st = notAuthenticated
	return ok(c.tag, "LOGOUT completed").
		extra("BYE IMAP4rev1 Server logging out").
		shouldClose()
}

//------------------------------------------------------------------------------

// selectMailbox is a SELECT command
type selectMailbox struct {
	tag     string
	mailbox string
}

// execute a SELECT command
func (c *selectMailbox) execute(sess *session) *response {

	// Is the user authenticated?
	if sess.st < authenticated {
		return mustAuthenticate(sess, c.tag, "SELECT")
	}

	// Select the mailbox
	mbox := pathToSlice(c.mailbox)
	exists, err := sess.selectMailbox(mbox)

	if err != nil {
		return internalError(sess, c.tag, "SELECT", err)
	}

	if !exists {
		return no(c.tag, "SELECT No such mailbox")
	}

	// Build a response that includes mailbox information
	res := ok(c.tag, "[READ-WRITE] SELECT completed")

	err = sess.addMailboxInfo(res)

	if err != nil {
		return internalError(sess, c.tag, "SELECT", err)
	}

	return res
}

//------------------------------------------------------------------------------

type statusMailbox struct {
	tag     string
	mailbox string
	params  []string
}

// execute a STATUS command
func (c *statusMailbox) execute(sess *session) *response {

	// Is the user authenticated?
	if sess.st < authenticated {
		return mustAuthenticate(sess, c.tag, "STATUS")
	}

	// Status the mailbox
	mbox := pathToSlice(c.mailbox)
	exists, err := sess.statusMailbox(mbox)

	if err != nil {
		return internalError(sess, c.tag, "STATUS", err)
	}

	if !exists {
		return no(c.tag, "STATUS No such mailbox")
	}

	// Build a response that includes mailbox information
	res := ok(c.tag, "STATUS completed")

	err = sess.addStatusMailboxInfo(res, c.mailbox, c.params)
	if err != nil {
		return internalError(sess, c.tag, "STATUS", err)
	}

	return res
}

//------------------------------------------------------------------------------

// list is a LIST command
type list struct {
	tag         string
	reference   string // Context of mailbox name
	mboxPattern string // The mailbox name pattern
}

// execute a LIST command
func (c *list) execute(sess *session) *response {

	// Is the user authenticated?
	if sess.st < authenticated {
		return mustAuthenticate(sess, c.tag, "LIST")
	}

	// Is the mailbox pattern empty? This indicates that we should return
	// the delimiter and the root name of the reference
	if c.mboxPattern == "" {
		res := ok(c.tag, "LIST completed")
		res.extra(fmt.Sprintf(`LIST () "%s" %s`, pathDelimiter, c.reference))
		return res
	}

	// Convert the reference and mbox pattern into slices
	ref := pathToSlice(c.reference)
	mbox := pathToSlice(c.mboxPattern)

	// Get the list of mailboxes
	mboxes, err := sess.list(ref, mbox)

	if err != nil {
		return internalError(sess, c.tag, "LIST", err)
	}

	// Check for an empty response
	if len(mboxes) == 0 {
		return no(c.tag, "LIST no results")
	}

	// Respond with the mailboxes
	res := ok(c.tag, "LIST completed")
	for _, mbox := range mboxes {
		res.extra(fmt.Sprintf(`LIST (%s) "%s" %s`,
			joinMailboxFlags(mbox),
			string(pathDelimiter),
			strings.Join(mbox.Path, string(pathDelimiter))))
	}

	return res
}

//------------------------------------------------------------------------------

// unknown is an unknown/unsupported command
type unknown struct {
	tag string
	cmd string
}

// execute reports an error for an unknown command
func (c *unknown) execute(s *session) *response {
	message := fmt.Sprintf("%s unknown command", c.cmd)
	s.log(message)
	return bad(c.tag, message)
}

//------------------------------------------------------------------------------

type appendCmd struct {
	l             *lexer
	tag           string
	mailbox       string
	flags         []string
	dateTime      time.Time
	messageLength int64
	ready         bool
}

func (ac *appendCmd) execute(s *session) *response {
	var res *response

	switch ac.ready {
	case false:
		res = continuation("Ready for literal data")
		ac.ready = true
	case true:
		message, err := ac.l.literalRest(ac.messageLength)
		if err != nil {
			return no(ac.tag, fmt.Sprintf("Couldn't read message: %s", err))
		}
		err = s.append(ac.mailbox, ac.flags, ac.dateTime, message)
		if err != nil {
			log.Println("Couldn't append message:", err)
			return bad(ac.tag, "Couldn't APPENDing message")
		}
		res = ok(ac.tag, "APPEND completed")
	}

	return res
}

type searchCmd struct {
	l         *lexer
	tag       string
	returnUid bool

	// Progressively filled until we're ready to parse it all
	fullLine   []byte
	continuing bool
}

func (sc *searchCmd) execute(s *session) *response {

	if s.st < selected {
		return mustSelect(s, sc.tag, "SEARCH")
	}

	if sc.continuing {
		sc.l.newLine()
	}

	var res *response
	// Continue aggregating arguments
	// TODO: we really shouldn't access the lexer here...
	sc.fullLine = append(sc.fullLine, sc.l.line[sc.l.idx:]...)
	// Even dirtier: manually re-add linefeeds that have been deleted by
	// textproto
	sc.fullLine = append(sc.fullLine, lf)
	if sc.l.line[len(sc.l.line)-1] == rightCurly {
		sc.continuing = true
		return continuation("Continue")
	}
	sc.continuing = false

	args, err := aggregateSearchArguments(sc.fullLine)
	if err != nil {
		log.Println("Couldn't parse arguments:", err)
		return bad(sc.tag, "SEARCH error with args")
	}
	messages, err := s.search(args, sc.returnUid)
	if err != nil {
		log.Println("Search error:", err)
		return bad(sc.tag, "SEARCH internal error")
	}

	messagesAsStringList := make([]string, len(messages))
	for i := range messages {
		messagesAsStringList[i] = strconv.Itoa(messages[i])
	}
	res = ok(sc.tag, "SEARCH completed")
	res.extra("SEARCH " + strings.Join(messagesAsStringList, " "))
	// Do the actual search
	return res
}

type fetchCmd struct {
	tag     string
	useUids bool

	sequenceSet string
	args        []fetchArgument
}

type messageFetchResponse struct {
	id    string
	items []fetchItem
}

type fetchItem struct {
	key    string
	values []string
}

func (fc *fetchCmd) execute(s *session) *response {
	if s.st < selected {
		return mustSelect(s, fc.tag, "FETCH")
	}
	if fc.useUids {
		fc.args = append(fc.args, fetchArgument{text: "UID"})
	}
	result, err := s.fetch(fc.sequenceSet, fc.args, fc.useUids)
	if err != nil {
		log.Printf("Error fetching %s with sequenceSet %q with useUids at %t\n", s.mailbox.Id, fc.sequenceSet, fc.useUids)
		log.Printf("Args were %q\n", fc.args)
		log.Println(err)
		return bad(fc.tag, "FETCH internal error")
	}

	res := ok(fc.tag, "SEARCH")
	for _, messageResponse := range result {
		lineElems := make([]string, 0)
		for _, item := range messageResponse.items {
			var value string
			if len(item.values) == 1 {
				value = item.values[0]
			} else {
				value = "(" + strings.Join(item.values, " ") + ")"
			}
			lineElems = append(lineElems, item.key+" "+value)
		}
		res.extra(messageResponse.id + " FETCH " + "(" + strings.Join(lineElems, " ") + ")")
	}
	return res
}

//------ Helper functions ------------------------------------------------------

// internalError logs an error and return an response
func internalError(sess *session, tag string, commandName string, err error) *response {
	message := commandName + " " + err.Error()
	sess.log(message)
	return no(tag, message).shouldClose()
}

// mustAuthenticate indicates a command is invalid because the user has not authenticated
func mustAuthenticate(sess *session, tag string, commandName string) *response {
	message := commandName + " not authenticated"
	sess.log(message)
	return bad(tag, message)
}

// mustSelect indicates a command is invalid because the user has no
// mailbox selected
func mustSelect(sess *session, tag string, commandName string) *response {
	message := commandName + " not selected"
	sess.log(message)
	return bad(tag, message)
}

// pathToSlice converts a path to a slice of strings
func pathToSlice(path string) []string {

	// Split the path
	ret := strings.Split(path, string(pathDelimiter))

	if len(ret) == 0 {
		return ret
	}

	// Remove leading and trailing blanks
	if ret[0] == "" {
		if len(ret) > 1 {
			ret = ret[1:]
		} else {
			return []string{}
		}
	}

	lastIndex := len(ret) - 1
	if ret[lastIndex] == "" {
		if len(ret) > 1 {
			ret = ret[0:lastIndex]
		} else {
			return []string{}
		}
	}

	return ret

}

// joinMailboxFlags returns a string of mailbox flags for the given mailbox
func joinMailboxFlags(m *Mailbox) string {

	// Convert the mailbox flags into a slice of strings
	flags := make([]string, 0, 4)

	for flag, str := range mailboxFlags {
		if m.Flags&flag != 0 {
			flags = append(flags, str)
		}
	}

	// Return a joined string
	return strings.Join(flags, ",")
}
