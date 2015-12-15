package unpeu

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vova616/xxhash"
)

var keywordToTag = map[string]string{
	"ANSWERED": "tag:answered",
	"DELETED":  "tag:deleted",
	"FLAGGED":  "tag:starred",
	"SEEN":     "-tag:unread",

	"UNSANSWERED": "-tag:answered",
	"UNDELETED":   "-tag:deleted",
	"UNFLAGGED":   "-tag:starred",
	"UNSEEN":      "tag:unread",
}

var mailboxToNotmuchMapping = map[string]string{
	"INBOX":      "inbox",
	"\\Flagged":  "starred",
	"\\Deleted":  "deleted",
	"\\Draft":    "draft",
	"\\Answered": "answered",
}

var tagToKeyword = reverse(mailboxToNotmuchMapping)

func reverse(in map[string]string) map[string]string {
	out := make(map[string]string)
	for k, v := range in {
		out[v] = k
	}
	return out
}

var _ Mailstore = &NotmuchMailstore{}

type NotmuchMailstore struct {
	l sync.RWMutex

	// This cache protects ALL entries beyond. It must be used as soon as
	// any of them is used or modified, and all entries must be cleared as
	// soon as a change is detected, so they can be repopulated on the
	// next call to the relevant function
	cache       sync.RWMutex
	midToUidMap map[string]int
	uidToMidMap []string
}

func (nm *NotmuchMailstore) GetMailbox(path []string) (*Mailbox, error) {
	// Get UUID
	rd, err := nm.raw("count", "--lastmod")
	if err != nil {
		return nil, err
	}
	line, err := ioutil.ReadAll(rd)
	if err != nil {
		return nil, err
	}
	rd.Close()
	parts := strings.Split(string(line), "\t")
	if len(parts) != 3 {
		return nil, fmt.Errorf("Invalid UIDVALIDITY")
	}
	uidValidity := xxhash.Checksum32([]byte(parts[1]))

	return &Mailbox{
		Name:        strings.Join(path, "/"),
		Path:        path,
		Id:          Id(strings.Join(path, "/")),
		Flags:       Noinferiors,
		UidValidity: uidValidity,
	}, nil
}

func (nm *NotmuchMailstore) GetMailboxes(path []string) ([]*Mailbox, error) {
	if len(path) > 0 {
		return nil, nil
	}
	rd, err := nm.raw("search", "--output=tags", "--format=json", "*")
	if err != nil {
		return nil, err
	}
	var mailboxNames []string
	err = json.NewDecoder(rd).Decode(&mailboxNames)
	if err != nil {
		return nil, err
	}
	rd.Close()
	sort.Strings(mailboxNames)

	var mailboxes []*Mailbox
	for _, mb := range mailboxNames {
		if mb == "inbox" {
			mb = "INBOX"
		}
		mailboxes = append(mailboxes, &Mailbox{
			Name:  mb,
			Path:  []string{mb},
			Id:    Id(mb),
			Flags: Noinferiors,
		})
	}
	return mailboxes, nil
}

func (nm *NotmuchMailstore) FirstUnseen(mbox Id) (int64, error) {
	// RFC says it's ok to not return first unseed, client should get what
	// it wants through a SEARCH
	return 0, nil
}

func (nm *NotmuchMailstore) CountUnseen(mbox Id) (int64, error) {
	rd, err := nm.raw("count", "tag:"+string(mbox), "AND", "tag:unread")
	if err != nil {
		return 0, err
	}
	asBytes, err := ioutil.ReadAll(rd)
	if err != nil {
		return 0, err
	}
	rd.Close()

	// elide final cr or lf
	asString := strings.TrimRight(string(asBytes), "\r\n")
	asInt, err := strconv.Atoi(asString)
	return int64(asInt), err
}

func (nm *NotmuchMailstore) TotalMessages(mbox Id) (int64, error) {
	search := "tag:" + string(mbox)
	if mbox == "" {
		search = ""
	}
	rd, err := nm.raw("count", search)
	if err != nil {
		return 0, err
	}
	asBytes, err := ioutil.ReadAll(rd)
	if err != nil {
		return 0, err
	}
	rd.Close()

	// elide final cr or lf
	asString := strings.TrimRight(string(asBytes), "\r\n")
	asInt, err := strconv.Atoi(asString)
	return int64(asInt), err
}

func (nm *NotmuchMailstore) RecentMessages(mbox Id) (int64, error) {
	// Useless
	return 0, nil
}

func (nm *NotmuchMailstore) NextUid(mbox Id) (int64, error) {
	// RFC says that UIDNEXT MUST NOT increment if no message was added to
	// this mailbox, so we can't just use the total number of messages.
	// Moreover it MUST increment, so we can't just use the number of
	// messages in this box + 1. Currently we use the index of the message
	// in the overall list of ALL messages, regardless of the tags it has,
	// so this means we can't predict what the next one will be...
	// Fortunately the RFC allows us to not predict a UIDNEXT.
	return 0, nil
}

func (nm *NotmuchMailstore) AppendMessage(mailbox string, flags []string, dateTime time.Time, message string) error {
	// Prepare tags to add
	tags := make([]string, 0, len(flags))
	var seen bool
	for _, t := range flags {
		if t != "" {
			if mapping, ok := mailboxToNotmuchMapping[t]; ok {
				t = mapping
			}
			if t == "\\Seen" {
				seen = true
				continue
			}
			tags = append(tags, "+"+t)
		}
	}
	if !seen {
		tags = append(tags, "+unread")
	}
	if mailbox == "INBOX" {
		tags = append(tags, "+inbox")
	} else {
		tags = append(tags, "+"+mailbox)
	}

	maildir := os.Getenv("NOTMUCH_MAILDIR")
	if maildir == "" {
		return fmt.Errorf("Missing maildir, use the NOTMUCH_MAILDIR env variable")
	}

	args := []string{"insert", "--folder=" + maildir, "+new"}
	args = append(args, tags...)
	cmd, err := nm.rawWrite(args...)
	if err != nil {
		return err
	}
	//log.Println("Adding with command:", cmd.cmd.Args)
	_, err = io.WriteString(cmd, message)
	if err != nil {
		log.Println("Error writing message:", err)
	}
	return cmd.Close()
}

func (nm *NotmuchMailstore) Search(mailbox Id, args []searchArgument, returnUid bool) (ids []int, err error) {
	args = append(args, searchArgument{key: "KEYWORD", values: []string{string(mailbox)}})
	notmuchQuery := parseSearchArguments(args)
	// Remove top-level parenthesis
	notmuchQuery = notmuchQuery[1 : len(notmuchQuery)-1]
	rd, err := nm.raw("search", "--format=json", "--output=messages", "--sort=oldest-first", notmuchQuery)
	if err != nil {
		return nil, err
	}

	var mids []string
	err = json.NewDecoder(rd).Decode(&mids)
	if err != nil {
		return nil, err
	}
	rd.Close()

	if !returnUid {
		return nil, fmt.Errorf("Can't output sequence id, we only understand uids")
	}
	ids = make([]int, 0, len(mids))
	midToUid, err := nm.midToUid()
	if err != nil {
		return nil, err
	}
	for _, mid := range mids {
		ids = append(ids, midToUid[mid])
	}
	return ids, nil
}

func parseSearchArguments(args []searchArgument) string {
	query := make([]string, 0)
	for _, arg := range args {
		switch arg.key {
		case "ALL":
			// Nothing special here
			continue
		case "NEW", "OLD", "RECENT", "HEADER", "SMALLER", "LARGER", "SEQUENCESET", "UID":
			log.Println(arg.key, "is not supported")
			// Not supported
			continue
		case "ANSWERED", "DELETED", "FLAGGED", "SEEN", "DRAFT",
			"UNSANSWERED", "UNDELETED", "UNFLAGGED", "UNSEEN", "UNDRAFT":
			query = append(query, keywordToTag[arg.key])
		case "KEYWORD":
			query = append(query, "tag:"+arg.values[0])
		case "UNKEYWORD":
			query = append(query, "-tag:"+arg.values[0])
		case "FROM":
			query = append(query, "from:"+arg.values[0])
		case "TO", "CC", "BCC":
			// TODO: postprocess on CC and BCC
			query = append(query, "to:"+arg.values[0])

		// Internal date is the date the server received the message. So
		// this is technically wrong.
		case "SENTON", "ON":
			query = append(query, "date:"+arg.values[0]+"..!")
		case "SENTSINCE", "SINCE":
			query = append(query, "date:"+arg.values[0]+"..")
		case "SENTBEFORE", "BEFORE":
			query = append(query, "date:.."+arg.values[0])

		case "SUBJECT":
			query = append(query, `subject:`+quote(arg.values[0]))
		case "BODY", "TEXT": // Technically wrong, but matches in most interesting cases
			query = append(query, quote(arg.values[0]))

		}

		if arg.group {
			sub := parseSearchArguments(arg.children)
			if len(arg.children) == 1 {
				// elide parenthesis
				sub = sub[1 : len(sub)-1]
			}
			query = append(query, sub)
		}

		if arg.or {
			left := parseSearchArguments([]searchArgument{arg.children[0]})
			right := parseSearchArguments([]searchArgument{arg.children[1]})
			full := strings.Join([]string{left, right}, " OR ")
			query = append(query, full)
		}

		if len(query) == 0 {
			continue
		}
		justAdded := query[len(query)-1]
		if arg.not {
			if justAdded[0] == '-' {
				justAdded = justAdded[1:]
			} else {
				justAdded = "-" + justAdded
			}
			query[len(query)-1] = justAdded
		}
	}

	// TODO: post-process for SMALLER and LARGER
	// TODO: post-process for sequence set matching

	if len(query) == 0 {
		query = []string{"*"}
	}
	return "(" + strings.Join(query, " ") + ")"
}

func (nm *NotmuchMailstore) Fetch(mailbox Id, sequenceSet string, args []fetchArgument, useUids bool) ([]messageFetchResponse, error) {
	cmd, err := nm.raw("search", "--sort=oldest-first", "--format=json", "--output=messages", "tag:"+string(mailbox))
	if err != nil {
		return nil, err
	}

	var mailboxMessageIds []string
	err = json.NewDecoder(cmd).Decode(&mailboxMessageIds)
	if err != nil {
		cmd.Close()
		return nil, err
	}
	cmd.Close()

	// Transform sequence set into usable list of ids
	var max int
	var uidToMid []string
	if useUids {
		uidToMid, err = nm.uidToMid()
		if err != nil {
			return nil, err
		}
		max = len(uidToMid)
	} else {
		max64, err := nm.TotalMessages(mailbox)
		if err != nil {
			return nil, err
		}
		max = int(max64)
	}
	inputAsList, err := toList(sequenceSet, max)
	if err != nil {
		return nil, err
	}

	allResults := make([]messageFetchResponse, 0, len(inputAsList))
	if useUids {
		// Build message-id -> sequence id map
		midToSeqId := make(map[string]int)
		for index, mailboxMessageId := range mailboxMessageIds {
			midToSeqId[mailboxMessageId] = index + 1
		}

		for _, uid := range inputAsList {
			if uid > len(uidToMid)-1 {
				continue
			}
			mid := uidToMid[uid]
			sequenceId, ok := midToSeqId[mid]
			if !ok {
				continue
			}
			items, err := nm.fetchMessageItems(mid, args)
			if err != nil {
				return nil, err
			}
			allResults = append(allResults, messageFetchResponse{
				id:    strconv.Itoa(sequenceId),
				items: items,
			})
		}
	} else {
		for _, id := range inputAsList {
			if id-1 < 0 || id-1 > len(mailboxMessageIds)-1 {
				return nil, fmt.Errorf("Invalid id %d when we have %d messages", id, len(mailboxMessageIds))
			}
			mid := mailboxMessageIds[id-1]
			items, err := nm.fetchMessageItems(mid, args)
			if err != nil {
				return nil, err
			}
			allResults = append(allResults, messageFetchResponse{id: strconv.Itoa(id), items: items})
		}
	}

	return allResults, nil
}

// A message as retrieved from notmuch
// See the devel/schemata file in notmuch source
// http://git.notmuchmail.org/git/notmuch/blob/HEAD:/devel/schemata
type Message struct {
	Id           string        `json:"id"`
	DateRelative string        `json:"date_relative"`
	Tags         []string      `json:"tags"`
	Header       MessageHeader `json:"headers"`

	Body []MessagePart
}

type MessageHeader struct {
	Subject string `json:"subject"`
	From    string `json:"from"`
	To      string `json:"to"`
	Cc      string `json:"cc"`
	Bcc     string `json:"bcc"`
	ReplyTo string `json:"reply-to"`

	// Format is "Mon, 2 Jan 2006 15:04:05 -0700"
	Date string `json:"date"`
}

/*

notmuch schema of a message

  57 # A message (format_part_sprinter)
  58 message = {
  59     # (format_message_sprinter)
  60     id:             messageid,
  61     match:          bool,
  62     filename:       string,
  63     timestamp:      unix_time, # date header as unix time
  64     date_relative:  string,   # user-friendly timestamp
  65     tags:           [string*],
  66
  67     headers:        headers,
  68     body?:          [part]    # omitted if --body=false
  69 }

*/

type MessagePart struct {
	ContentType   string `json:"content-type"`
	ContentId     string `json:"content-id"`
	ContentLength int    `json:"content-length"`

	// Can either be:
	// - a string(raw content)
	// - a []MessagePart if ContentType starts with "multipart/"
	// - a [{headers: MessageHeader, body: []MessagePart}] if ContentType
	//   is "message/rfc822"
	Content interface{} `json:"content"`
}

/*

notmuch schema of a part

  71 # A MIME part (format_part_sprinter)
  72 part = {
  73     id:             int|string, # part id (currently DFS part number)
  74
  75     encstatus?:     encstatus,
  76     sigstatus?:     sigstatus,
  77
  78     content-type:   string,
  79     content-id?:    string,
  80     # if content-type starts with "multipart/":
  81     content:        [part*],
  82     # if content-type is "message/rfc822":
  83     content:        [{headers: headers, body: [part]}],
  84     # otherwise (leaf parts):
  85     filename?:      string,
  86     content-charset?: string,
  87     # A leaf part's body content is optional, but may be included if
  88     # it can be correctly encoded as a string.  Consumers should use
  89     # this in preference to fetching the part content separately.
  90     content?:       string,
  91     # If a leaf part's body content is not included, the length of
  92     # the encoded content (in bytes) may be given instead.
  93     content-length?: int,
  94     # If a leaf part's body content is not included, its transfer encoding
  95     # may be given.  Using this and the encoded content length, it is
  96     # possible for the consumer to estimate the decoded content length.
  97     content-transfer-encoding?: string
  98 }
  99
*/

func (nm *NotmuchMailstore) fetchMessageItems(mid string, args []fetchArgument) ([]fetchItem, error) {
	cmd, err := nm.raw("show", "--format=json", "--part=0", "id:"+mid)
	if err != nil {
		return nil, err
	}
	var msg Message
	err = json.NewDecoder(cmd).Decode(&msg)
	cmd.Close()
	if err != nil {
		return nil, err
	}

	result := make([]fetchItem, 0)
	messageParsers := make([]messageParser, 0)

	midToUid, err := nm.midToUid()
	if err != nil {
		return nil, err
	}
	for _, arg := range args {
		switch arg.text {
		case "UID":
			uid := midToUid[msg.Id]
			result = append(result, fetchItem{key: "UID", values: []string{strconv.Itoa(uid)}})
		case "FLAGS":
			flags := make([]string, 0, len(msg.Tags))
			var unread bool
			for _, tag := range msg.Tags {
				if keyword, ok := tagToKeyword[tag]; ok {
					flags = append(flags, keyword)
				} else if tag == "unread" {
					unread = true
					continue
				} else {
					flags = append(flags, tag)
				}
			}
			if !unread {
				flags = append(flags, "\\Seen")
			}
			result = append(result, fetchItem{key: "FLAGS", values: flags})
		case "INTERNALDATE":
			date, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", msg.Header.Date)
			if err != nil {
				return nil, err
			}
			outDate := date.Format("02-Jan-2006 15:04:05 -0700")
			result = append(result, fetchItem{key: "INTERNALDATE", values: []string{quote(outDate)}})
		case "RFC822.HEADER":
			messageParsers = append(messageParsers, &rfc822headerParser{})
		case "RFC822.SIZE":
			messageParsers = append(messageParsers, &rfc822sizeParser{})
		case "RFC822.TEXT":
			messageParsers = append(messageParsers, &rfc822textParser{})
		case "ENVELOPE":
			messageParsers = append(messageParsers, &envelopeParser{})
		case "RFC822":
			messageParsers = append(messageParsers, &rfc822fullParser{})
		default:
			log.Printf("%s is not handled yet\n", arg.text)
		}
	}

	if len(messageParsers) > 0 {
		writers := make([]io.Writer, 0, len(messageParsers))
		dones := make([]chan error, 0, len(messageParsers))

		for _, mp := range messageParsers {
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			dones = append(dones, done)
			go func(mp messageParser) {
				done <- mp.read(pr)
				// A message parser may stop reading before the end, finish it
				// off
				io.Copy(ioutil.Discard, pr)
				close(done)
			}(mp)
			writers = append(writers, pw)
		}

		mw := io.MultiWriter(writers...)
		cmd, err := nm.raw("show", "--format=raw", "--part=0", "id:"+mid)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(mw, cmd)
		cmd.Close()
		if err != nil {
			return nil, err
		}

		for i, done := range dones {
			writers[i].(io.Closer).Close()
			err := <-done
			if err != nil {
				parser := messageParsers[i]
				return nil, fmt.Errorf("Error extracting field %q: %s", parser.getKey(), err)
			}
		}

		for _, mp := range messageParsers {
			result = append(result, fetchItem{key: mp.getKey(), values: mp.getValues()})
		}
	}

	/*
			 BODYSTRUCTURE
		         The [MIME-IMB] body structure of the message.  This is computed
		         by the server by parsing the [MIME-IMB] header fields in the
		         [RFC-2822] header and [MIME-IMB] headers.

	*/

	return result, nil
}

// -----------------
//  Message parsers
// -----------------

// A parser that needs the full body of the message to work
type messageParser interface {
	read(io.Reader) error

	getKey() string

	// Valid only after the full message has been written
	getValues() []string
}

// RFC822.SIZE
type rfc822sizeParser struct {
	size int
}

func (sp *rfc822sizeParser) read(r io.Reader) error {
	n, err := io.Copy(ioutil.Discard, r)
	if err != nil {
		return err
	}
	sp.size = int(n)
	return nil
}

func (sp *rfc822sizeParser) getKey() string      { return "RFC822.SIZE" }
func (sp *rfc822sizeParser) getValues() []string { return []string{strconv.Itoa(sp.size)} }

// RFC822.HEADER
type rfc822headerParser struct {
	header string
}

func (hp *rfc822headerParser) read(r io.Reader) error {
	var hdr bytes.Buffer
	buf := bufio.NewReader(io.TeeReader(r, &hdr))
	headerReader := textproto.NewReader(buf)
	_, err := headerReader.ReadMIMEHeader()
	if err != nil {
		return err
	}

	// Don't forget to elide the last bytes that were read but are not
	// part of the header
	hp.header = string(hdr.Bytes()[:hdr.Len()-buf.Buffered()])

	return nil
}

func (hp *rfc822headerParser) getKey() string      { return "RFC822.HEADER" }
func (hp *rfc822headerParser) getValues() []string { return []string{literalify(hp.header)} }

// RFC822.TEXT
type rfc822textParser struct {
	text bytes.Buffer
}

func (tp *rfc822textParser) read(r io.Reader) error {
	buf := bufio.NewReader(r)
	headerReader := textproto.NewReader(buf)
	_, err := headerReader.ReadMIMEHeader()
	if err != nil {
		return err
	}

	// Write the bytes that have been buffered but are not part of the
	// header
	_, err = io.Copy(&tp.text, buf)
	if err != nil {
		return err
	}
	_, err = io.Copy(&tp.text, r)
	return err
}

func (tp *rfc822textParser) getKey() string      { return "RFC822.TEXT" }
func (tp *rfc822textParser) getValues() []string { return []string{literalify(string(tp.text.Bytes()))} }

// ENVELOPE
type envelopeParser struct {
	fields []string
}

func (ep *envelopeParser) read(r io.Reader) error {
	tpReader := textproto.NewReader(bufio.NewReader(r))
	hdr, err := tpReader.ReadMIMEHeader()
	if err != nil {
		return err
	}

	messageId := hdr.Get("Message-Id")
	if messageId[0] == lessThan && messageId[len(messageId)-1] == moreThan {
		messageId = messageId[1 : len(messageId)-1]
	}
	// Technically if a field doesn't exist the corresponding value should
	// be NIL; only if it exists AND is empty should it be set to "".
	ep.fields = []string{
		quote(hdr.Get("Date")), literalify(hdr.Get("Subject")),
		addresses(hdr, "From"), addresses(hdr, "Sender"), addresses(hdr, "Reply-To"), addresses(hdr, "To"), addresses(hdr, "Cc"), addresses(hdr, "Bcc"),
		quote(hdr.Get("In-Reply-To")), quote(messageId),
	}
	return nil
}

func (ep *envelopeParser) getKey() string      { return "ENVELOPE" }
func (ep *envelopeParser) getValues() []string { return ep.fields }

// RFC822
type rfc822fullParser struct {
	full bytes.Buffer
}

func (fp *rfc822fullParser) read(r io.Reader) error {
	_, err := io.Copy(&fp.full, r)
	return err
}

func (fp *rfc822fullParser) getKey() string      { return "RFC822" }
func (fp *rfc822fullParser) getValues() []string { return []string{literalify(string(fp.full.Bytes()))} }

// ---------------------------
//          Helpers
// ---------------------------

func literalify(in string) string {
	return fmt.Sprintf("{%d}\r\n%s", len(in), in)
}

func (nm *NotmuchMailstore) uidToMid() ([]string, error) {
	nm.cache.RLock()
	defer nm.cache.RUnlock()
	if nm.uidToMidMap != nil {
		return nm.uidToMidMap, nil
	}
	cmd, err := nm.raw("search", "--output=messages", "--sort=oldest-first", "--format=json", "*")
	if err != nil {
		return nil, err
	}

	var ids []string
	err = json.NewDecoder(cmd).Decode(&ids)
	cmd.Close()

	// Ids start at 1 so we need to shift them to the right
	ids = append(ids, "")
	copy(ids[1:], ids[0:])
	nm.uidToMidMap = ids
	return ids, err
}

func (nm *NotmuchMailstore) midToUid() (map[string]int, error) {
	nm.cache.RLock()
	defer nm.cache.RUnlock()
	if nm.midToUidMap != nil {
		return nm.midToUidMap, nil
	}
	cmd, err := nm.raw("search", "--output=messages", "--sort=oldest-first", "--format=json", "*")
	if err != nil {
		return nil, err
	}
	var ids []string
	err = json.NewDecoder(cmd).Decode(&ids)
	cmd.Close()
	if err != nil {
		return nil, err
	}
	midToUidMap := make(map[string]int)
	for i, id := range ids {
		midToUidMap[id] = i + 1
	}
	nm.midToUidMap = midToUidMap
	return midToUidMap, err
}

func quote(in string) string {
	return `"` + in + `"`
}

func addresses(hdr map[string][]string, key string) string {
	vals := hdr[textproto.CanonicalMIMEHeaderKey(key)]
	if len(vals) == 0 {
		return "NIL"
	}

	addresses := make([]string, 0)
	for _, val := range vals {
		addr, err := mail.ParseAddress(val)
		if err != nil {
			continue
		}
		addrParts := strings.Split(addr.Address, "@")
		if len(addrParts) != 2 {
			continue
		}

		parts := []string{quote(addr.Name), "NIL", quote(addrParts[0]), quote(addrParts[1])}
		address := `(` + strings.Join(parts, " ") + `)`
		addresses = append(addresses, address)
	}
	return `(` + strings.Join(addresses, " ") + `)`
}

// ---------------------------
//      Notmuch wrapper
// ---------------------------

// A wrapper around a shell command that implements io.Read, io.Write and
// io.Close.
// io.Read will read from the command's stdoutpipe and io.Write will
// write to the command's stdinpipe. io.Close will close the command,
// properly closing all resources. A notmuchCommand  MUST be closed,
// otherwise resources are leaked
type writingNotmuchCommand struct {
	l    *sync.RWMutex
	args []string
	cmd  *exec.Cmd
	io.Reader
	io.WriteCloser
}

func (c writingNotmuchCommand) Close() error {
	err := c.WriteCloser.Close()
	if err == nil {
		err = c.cmd.Wait()
	}
	if err != nil {
		log.Println("Error closing", c.args, ": ", err)
	}
	c.l.Unlock()
	return err
}

func (nm *NotmuchMailstore) rawWrite(args ...string) (writingNotmuchCommand, error) {
	nm.l.Lock()
	cmd := exec.Command(
		"notmuch",
		args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return writingNotmuchCommand{}, err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return writingNotmuchCommand{}, err
	}
	err = cmd.Start()
	if err != nil {
		return writingNotmuchCommand{}, err
	}

	return writingNotmuchCommand{
		l:           &nm.l,
		args:        args,
		cmd:         cmd,
		Reader:      out,
		WriteCloser: in,
	}, nil

}

// Same thing without the writing part
type notmuchCommand struct {
	l    *sync.RWMutex
	args []string
	cmd  *exec.Cmd
	io.Reader
}

func (c notmuchCommand) Close() error {
	err := c.cmd.Wait()
	if err != nil {
		log.Println("Error closing", c.args, ": ", err)
	}
	c.l.RUnlock()
	return err
}

func (nm *NotmuchMailstore) raw(args ...string) (notmuchCommand, error) {
	nm.l.RLock()
	cmd := exec.Command(
		"notmuch",
		args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return notmuchCommand{}, err
	}
	err = cmd.Start()
	if err != nil {
		return notmuchCommand{}, err
	}

	return notmuchCommand{
		l:      &nm.l,
		args:   args,
		cmd:    cmd,
		Reader: out,
	}, nil
}
