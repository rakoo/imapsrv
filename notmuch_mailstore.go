package unpeu

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
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
	cache        sync.RWMutex
	threadsCache map[string][]Message

	midToUidMap map[string]int
	uidToMidMap []string
}

func NewNotmuchMailstore() *NotmuchMailstore {
	nm := &NotmuchMailstore{}

	//nm.threads("*")
	// Cache those 2 because they're huge
	//nm.midToUid()
	//nm.uidToMid()
	return nm
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

	id := Id(strings.Join(path, "/"))
	if id == Id("INBOX") {
		id = Id("inbox")
	}
	return &Mailbox{
		Name:        strings.Join(path, "/"),
		Path:        path,
		Id:          id,
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
	threads, err := nm.threads("tag:" + string(mbox))
	if err != nil {
		return 0, err
	}

	flat := flatten(threads)
	var count int64
	for _, message := range flat {
		for _, tag := range message.Tags {
			if tag == "unread" {
				count++
				break
			}
		}
	}
	return count, nil
}

func (nm *NotmuchMailstore) TotalMessages(mbox Id) (int64, error) {
	messages, err := nm.messageIds(mbox)
	return int64(len(messages)), err
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

func (nm *NotmuchMailstore) Search(mailbox Id, args []searchArgument, returnUid, returnThreads bool) (threadMembers []threadMember, err error) {
	args = append(args, searchArgument{key: "KEYWORD", values: []string{string(mailbox)}})
	notmuchQuery, mode := parseSearchArguments(args)
	// Remove top-level parenthesis
	notmuchQuery = notmuchQuery[1 : len(notmuchQuery)-1]

	var midToUid map[string]int
	var midToSequenceId map[string]int
	switch returnUid {
	case true:
		midToUid = nm.midToUid()
	case false:
		allMessageIds, err := nm.messageIds(mailbox)
		if err != nil {
			return nil, err
		}
		midToSequenceId = make(map[string]int)
		for i, messageId := range allMessageIds {
			midToSequenceId[messageId] = i + 1
		}
	}

	if returnThreads && (mode == "" || mode != "REFS") {
		return nil, fmt.Errorf("Invalid mode for thread command")
	}
	var tids []string
	err = nm.json(&tids, "search", "--format=json", "--output=threads", "--sort=oldest-first", notmuchQuery)
	if err != nil {
		return nil, err
	}

	var getIdMapping func(messageId string) int
	switch returnUid {
	case true:
		getIdMapping = func(messageId string) int {
			id, ok := midToUid[messageId]
			if !ok {
				log.Println("Couldn't find", messageId, "in", mailbox)
			}
			return id
		}
	case false:
		getIdMapping = func(messageId string) int {
			id, ok := midToSequenceId[messageId]
			if !ok {
				log.Println("Couldn't find", messageId, "in", mailbox)
			}
			return id
		}
	}

	threadMembers = make([]threadMember, 0, len(tids))
	for _, tid := range tids {
		var result []interface{}
		err = nm.json(&result, "show", "--format=json", "--body=false", "--", "thread:"+tid)
		if err != nil {
			return nil, err
		}
		for _, thread := range result {
			topLevelMessages := thread.([]interface{})
			threadRoot := transformMessage(topLevelMessages[0], getIdMapping)
			if len(topLevelMessages) > 1 {
				for _, topLevelMessage := range topLevelMessages[1:] {
					directChild := transformMessage(topLevelMessage, getIdMapping)
					threadRoot.children = append(threadRoot.children, directChild)
				}
			}
			threadMembers = append(threadMembers, threadRoot)
		}
	}

	if returnThreads {
		return threadMembers, nil
	}

	// Flatten threadMembers
	// Sorting by id is the same as sorting by date, because mid->(u)id
	// mappings are already sorted
	flat := make([]threadMember, 0)
	var flatten func(ids []threadMember, id threadMember) []threadMember
	flatten = func(ids []threadMember, id threadMember) []threadMember {
		ids = append(ids, id)
		for _, child := range id.children {
			ids = flatten(ids, child)
		}
		return ids
	}
	for _, threadRoot := range threadMembers {
		flat = flatten(flat, threadRoot)
	}
	sort.Sort(byId(flat))
	return flat, nil
}

// We know the notmuch output structure, and it's not going to change,
// so we can bypass cast verification
func transformMessage(message interface{}, getIdMapping func(messageId string) int) threadMember {
	actual := message.([]interface{})
	header := actual[0].(map[string]interface{})
	tm := threadMember{
		id:       getIdMapping(header["id"].(string)),
		children: make([]threadMember, 0),
	}

	children := actual[1].([]interface{})
	for _, child := range children {
		childMessage := transformMessage(child, getIdMapping)
		tm.children = append(tm.children, childMessage)
	}
	return tm
}

func parseSearchArguments(args []searchArgument) (queryString string, threadMode string) {
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
		case "REFERENCES", "REFS", "ORDEREDSUBJECT":
			threadMode = arg.key
		}

		if arg.group {
			sub, _ := parseSearchArguments(arg.children)
			if len(arg.children) == 1 {
				// elide parenthesis
				sub = sub[1 : len(sub)-1]
			}
			query = append(query, sub)
		}

		if arg.or {
			left, _ := parseSearchArguments([]searchArgument{arg.children[0]})
			right, _ := parseSearchArguments([]searchArgument{arg.children[1]})
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
	return "(" + strings.Join(query, " ") + ")", threadMode
}

func (nm *NotmuchMailstore) Fetch(mailbox Id, sequenceSet string, args []fetchArgument, useUids bool) ([]messageFetchResponse, error) {
	mailboxMessageIds, err := nm.messageIds(mailbox)
	if err != nil {
		return nil, err
	}

	// Transform sequence set into usable list of ids
	var max int
	var uidToMid []string
	if useUids {
		uidToMid = nm.uidToMid()
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
				return nil, fmt.Errorf("Couldn't fetch mid %s: %s", mid, err)
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
				return nil, fmt.Errorf("Couldn't fetch mid %s: %s", mid, err)
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

	// This one is used internally, it doesn't exist as is in the notmuch
	// data model
	Children []Message
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

func (nm *NotmuchMailstore) fetchMessageItems(mid string, args []fetchArgument) ([]fetchItem, error) {
	msg, err := nm.getMessage(mid)
	if err != nil {
		return nil, err
	}

	result := make([]fetchItem, 0)
	messageParsers := make([]messageParser, 0)

	midToUid := nm.midToUid()
	for _, arg := range args {
		switch arg.text {
		case "UID":
			uid := midToUid[msg.Id]
			result = append(result, fetchItem{key: "UID", value: strconv.Itoa(uid)})
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
			flagsString := fmt.Sprintf("(%s)", strings.Join(flags, " "))
			result = append(result, fetchItem{key: "FLAGS", value: flagsString})
		case "INTERNALDATE":
			date, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", msg.Header.Date)
			if err != nil {
				return nil, err
			}
			outDate := date.Format("02-Jan-2006 15:04:05 -0700")
			result = append(result, fetchItem{key: "INTERNALDATE", value: quote(outDate)})
		case "RFC822.SIZE":
			messageParsers = append(messageParsers, &rfc822sizeParser{})
		case "ENVELOPE":
			messageParsers = append(messageParsers, &envelopeParser{})
		case "BODY", "BODY.PEEK":
			item, err := nm.fetchBodyArg(arg, msg)
			if err != nil {
				log.Println(err)
				continue
			}
			result = append(result, item)
		case "BODYSTRUCTURE":
			cmdHeader, err := nm.raw("show", "--format=raw", "--part=0", "--entire-thread=false", "id:"+msg.Id)
			if err != nil {
				return nil, err
			}
			hdr, err := textproto.NewReader(bufio.NewReader(cmdHeader)).ReadMIMEHeader()
			cmdHeader.Close()
			if err != nil {
				return nil, err
			}

			// Get mime type, if it exists
			mediaType := "text/plain"
			var params map[string]string
			if hdr.Get("Content-Type") != "" {
				mediaType, params, err = mime.ParseMediaType(hdr.Get("Content-Type"))
				if err != nil {
					return nil, err
				}
			}

			cmd, err := nm.raw("show", "--format=raw", "--part=1", "--entire-thread=false", "id:"+msg.Id)
			if err != nil {
				return nil, err
			}
			body, err := parse(cmd, mediaType, params)
			cmd.Close()
			if err != nil {
				return nil, err
			}
			result = append(result, fetchItem{key: "BODYSTRUCTURE", value: body.structure()})
		default:
			mapping := map[string]string{
				"RFC822.HEADER": "HEADER",
				"RFC822.TEXT":   "TEXT",
				"RFC822":        "",
			}
			if section, ok := mapping[arg.text]; ok {
				item, err := nm.fetchBodyArg(fetchArgument{section: section}, msg)
				if err != nil {
					log.Println(err)
					continue
				}
				item.key = arg.text
				result = append(result, item)
				continue
			}
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
			result = append(result, fetchItem{key: mp.getKey(), value: mp.getValue()})
		}
	}

	return result, nil
}

func (nm *NotmuchMailstore) fetchBodyArg(arg fetchArgument, notmuchMsg Message) (fetchItem, error) {
	cmd, err := nm.raw("show", "--format=raw", "--part=0", "--entire-thread=false", "id:"+notmuchMsg.Id)
	if err != nil {
		return fetchItem{}, err
	}
	defer cmd.Close()

	var rd io.Reader = bufio.NewReader(cmd)

	// Skip to relevant part
	if len(arg.part) > 0 {
		msg, err := mail.ReadMessage(rd)
		if err != nil {
			return fetchItem{}, err
		}

		contentType := msg.Header.Get("Content-Type")
		for parts := arg.part; len(parts) > 0; parts = parts[1:] {
			mediaType, params, err := mime.ParseMediaType(contentType)
			if err != nil {
				return fetchItem{}, err
			}
			if !strings.HasPrefix(mediaType, "multipart/") {
				// Special-case:
				// Every message has at least one part, even if it is not multipart/*
				// We deal with the case where messages are not multipart/*, but a
				// client still asks for BODY[1], which is valid as per RFC, and
				// returns the whole text
				if len(arg.part) == 1 && arg.part[0] == 1 {
					break
				}
				return fetchItem{}, fmt.Errorf("Invalid hierarchy")
			}
			partReader := multipart.NewReader(msg.Body, params["boundary"])

			for part := parts[0]; part > 0; part-- {
				p, err := partReader.NextPart()
				if err != nil {
					return fetchItem{}, err
				}
				rd = p
				contentType = p.Header.Get("Content-Type")

				// Same as upstream for quoted-printable, if
				// Content-Transfer-Encoding is base64 we silently replace the
				// reader with one that decodes on-the-fly
				//
				// See https://golang.org/src/mime/multipart/multipart.go?s=3209:3362#L98

				const cte = "Content-Transfer-Encoding"
				if p.Header.Get(cte) == "base64" {
					p.Header.Del(cte)
					rd = base64.NewDecoder(base64.StdEncoding, rd)
				}
			}
		}
	}

	/*
		if arg.section != "" && arg.section != "MIME" {
			_, ok := container.(Message)
			if !ok {
				return fetchItem{}, fmt.Errorf("Invalid fetch of %s on a non-message", arg.section)
			}
		}
	*/

	// Kinda lame
	// Build a pattern that will be completed later
	partStrings := make([]string, len(arg.part))
	for i := range arg.part {
		partStrings[i] = strconv.Itoa(arg.part[i])
	}
	keyPattern := "BODY["
	if len(partStrings) > 0 {
		keyPattern += strings.Join(partStrings, ".")
	}
	if arg.section == "" || len(partStrings) == 0 {
		keyPattern += "%s]"
	} else {
		keyPattern += ".%s]"
	}
	if arg.offset >= 0 {
		keyPattern += "<" + strconv.Itoa(arg.offset)
	}
	if arg.length > 0 {
		keyPattern += "." + strconv.Itoa(arg.length)
	}
	if strings.Contains(keyPattern, "<") {
		keyPattern += ">"
	}

	var key string
	var value string
	switch arg.section {
	case "":
		key = fmt.Sprintf(keyPattern, "")

		fullBody, err := ioutil.ReadAll(rd)
		if err != nil {
			return fetchItem{}, err
		}
		value = string(fullBody)
	case "HEADER":
		key = fmt.Sprintf(keyPattern, "HEADER")

		var hdr bytes.Buffer
		buf := bufio.NewReader(io.TeeReader(rd, &hdr))
		headerReader := textproto.NewReader(buf)
		_, err := headerReader.ReadMIMEHeader()
		if err != nil {
			return fetchItem{}, err
		}

		// Don't forget to elide the last bytes that were read but are not
		// part of the header
		value = string(hdr.Bytes()[:hdr.Len()-buf.Buffered()])

	case "HEADER.FIELDS":
		key = fmt.Sprintf(keyPattern, "HEADER.FIELDS ("+strings.Join(arg.fields, " ")+")")

		// Build a fake header with only the given fields
		headerReader := textproto.NewReader(bufio.NewReader(rd))
		hdr, err := headerReader.ReadMIMEHeader()
		if err != nil {
			return fetchItem{}, err
		}

		fakeHeader := make([]string, 0, len(arg.fields)+1)
		for _, field := range arg.fields {
			vals := hdr[textproto.CanonicalMIMEHeaderKey(field)]
			if len(vals) > 0 {
				fakeHeader = append(fakeHeader, fmt.Sprintf("%s: %s", field, strings.Join(vals, ", ")))
			}
		}
		fakeHeader = append(fakeHeader, "\n")
		value = strings.Join(fakeHeader, "\n")
	case "HEADER.FIELDS.NOT":
		key = fmt.Sprintf(keyPattern, "HEADER.FIELDS.NOT ("+strings.Join(arg.fields, " ")+")")

		// Build a real header and remove the keys we don't want
		headerReader := textproto.NewReader(bufio.NewReader(rd))
		hdr, err := headerReader.ReadMIMEHeader()
		if err != nil {
			return fetchItem{}, err
		}

		for _, field := range arg.fields {
			hdr.Del(field)
		}
		serialized := make([]string, 0, len(hdr)+1)
		for k, values := range hdr {
			value := strings.Join(values, ", ")
			serialized = append(serialized, fmt.Sprintf("%s: %s", k, value))
		}
		serialized = append(serialized, "\n")
		value = strings.Join(serialized, "\n")
	case "TEXT":
		key = fmt.Sprintf(keyPattern, "TEXT")

		buf := bufio.NewReader(rd)
		headerReader := textproto.NewReader(buf)
		_, err := headerReader.ReadMIMEHeader()
		if err != nil {
			return fetchItem{}, err
		}

		// Write the bytes that have been buffered but are not part of the
		// header
		var text bytes.Buffer
		_, err = io.Copy(&text, buf)
		if err != nil {
			return fetchItem{}, err
		}
		_, err = io.Copy(&text, rd)
		if err != nil {
			return fetchItem{}, err
		}
		value = string(text.Bytes())
	case "MIME":
		return fetchItem{}, fmt.Errorf("MIME is unsupported")
	}

	// Subset value with offset and length
	from := 0
	if arg.offset != -1 {
		from = arg.offset
	}
	to := len(value)
	if arg.length != 0 {
		to = from + arg.length
	}
	subvalue := value[from:to]
	if to == from {
		subvalue = `""`
	} else {
		subvalue = literalify(subvalue)
	}

	item := fetchItem{
		key:   key,
		value: subvalue,
	}
	return item, nil
}

func (nm *NotmuchMailstore) Flag(mode flagMode, mbox Id, sequenceSet string, useUids bool, flags []string) ([]messageFetchResponse, error) {
	max, err := nm.TotalMessages(mbox)
	if err != nil {
		return nil, err
	}
	asList, err := toList(sequenceSet, int(max))
	if err != nil {
		return nil, err
	}

	mids := make([]string, 0, len(asList))

	if useUids {
		uidToMidList := nm.uidToMid()
		for _, uid := range asList {
			if uid > len(uidToMidList) {
				return nil, fmt.Errorf("Invalid message UID: %d", uid)
			}
			mids = append(mids, uidToMidList[uid])
		}
	} else {
		mailboxMessageIds, err := nm.messageIds(mbox)
		if err != nil {
			return nil, err
		}
		for _, sequenceId := range asList {
			if sequenceId > len(mailboxMessageIds) {
				return nil, fmt.Errorf("Invalid sequence id: %d (max is %d)", sequenceId, max)
			}
			mids = append(mids, mailboxMessageIds[sequenceId-1])
		}
	}

	allArgs := make([][]string, len(mids))
	for i, mid := range mids {
		msgArgs := make([]string, 0)
		switch mode {
		case SET:
			var seen bool
			for _, flag := range flags {
				if flag == "\\Seen" {
					seen = true
				}
				if flag == "\\Deleted" {
					continue
				}
			}

			if !seen {
				msgArgs = append(msgArgs, "+unread")
			}
			fallthrough
		case ADD:
			for _, flag := range flags {
				if flag == "\\Seen" {
					msgArgs = append(msgArgs, "-unread")
					continue
				}
				if flag == "\\Deleted" {
					msgArgs = append(msgArgs, "-"+string(mbox))
					continue
				}

				var keyword string
				var ok bool
				keyword, ok = mailboxToNotmuchMapping[flag]
				if !ok {
					keyword = flag
				}
				msgArgs = append(msgArgs, "+"+keyword)
			}
		case REMOVE:
			for _, flag := range flags {
				if flag == "\\Seen" {
					msgArgs = append(msgArgs, "+unread")
					continue
				}
				if flag == "\\Deleted" {
					msgArgs = append(msgArgs, "+"+string(mbox))
					continue
				}

				var keyword string
				var ok bool
				keyword, ok = mailboxToNotmuchMapping[flag]
				if !ok {
					keyword = flag
				}
				msgArgs = append(msgArgs, "-"+keyword)
			}
		}
		msgArgs = append(msgArgs, "--", "id:"+mid)

		allArgs[i] = msgArgs
	}

	if mode == SET {
		// No --batch support with --remove-all
		for _, msgArgs := range allArgs {
			// Prepend with the command
			msgArgs = append(msgArgs, "", "")
			copy(msgArgs[2:], msgArgs[0:])
			msgArgs[0] = "tag"
			msgArgs[1] = "--remove-all"
			cmd, err := nm.rawWrite(msgArgs...)
			if err != nil {
				return nil, err
			}
			err = cmd.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		tagArgs := []string{"tag", "--batch"}
		cmd, err := nm.rawWrite(tagArgs...)
		if err != nil {
			return nil, err
		}

		for _, msgArgs := range allArgs {
			io.WriteString(cmd, fmt.Sprintf("%s\n", strings.Join(msgArgs, " ")))
		}
		err = cmd.Close()
		if err != nil {
			return nil, err
		}
	}

	return nm.Fetch(mbox, sequenceSet, []fetchArgument{{text: "FLAGS"}}, useUids)
}

// -----------------
//  Message parsers
// -----------------

// A parser that needs the full body of the message to work
type messageParser interface {
	read(io.Reader) error

	getKey() string

	// Valid only after the full message has been written
	getValue() string
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

func (sp *rfc822sizeParser) getKey() string   { return "RFC822.SIZE" }
func (sp *rfc822sizeParser) getValue() string { return strconv.Itoa(sp.size) }

// ENVELOPE
type envelopeParser struct {
	envelope string
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
	fields := []string{
		quote(hdr.Get("Date")), literalify(hdr.Get("Subject")),
		addresses(hdr, "From"), addresses(hdr, "Sender"), addresses(hdr, "Reply-To"), addresses(hdr, "To"), addresses(hdr, "Cc"), addresses(hdr, "Bcc"),
		quote(hdr.Get("In-Reply-To")), quote(messageId),
	}
	ep.envelope = `(` + strings.Join(fields, " ") + `)`
	return nil
}

func (ep *envelopeParser) getKey() string   { return "ENVELOPE" }
func (ep *envelopeParser) getValue() string { return ep.envelope }

// ---------------------------
//          Helpers
// ---------------------------

func literalify(in string) string {
	return fmt.Sprintf("{%d}\r\n%s", len(in), in)
}

func (nm *NotmuchMailstore) uidToMid() []string {
	nm.cache.Lock()
	defer nm.cache.Unlock()
	if nm.uidToMidMap != nil {
		return nm.uidToMidMap
	}

	var mids []string
	err := nm.json(&mids, "search", "--format=json", "--output=messages", "--sort=oldest-first", "*")
	if err != nil {
		log.Println(err)
		return nil
	}

	mids = append(mids, "")
	copy(mids[1:], mids[0:])
	nm.uidToMidMap = mids
	return mids
}

func (nm *NotmuchMailstore) midToUid() map[string]int {
	nm.cache.Lock()
	defer nm.cache.Unlock()
	if nm.midToUidMap != nil {
		return nm.midToUidMap
	}

	var mids []string
	err := nm.json(&mids, "search", "--format=json", "--output=messages", "--sort=oldest-first", "*")
	if err != nil {
		log.Println(err)
		return nil
	}

	m := make(map[string]int)
	for i, mid := range mids {
		m[mid] = i + 1
	}

	nm.midToUidMap = m
	return m
}

func (nm *NotmuchMailstore) messageIds(mailboxId Id) ([]string, error) {
	threads, err := nm.threads("tag:" + string(mailboxId))
	if err != nil {
		return nil, err
	}
	flat := flatten(threads)
	sort.Sort(byDate(flat))

	var ids []string
	for _, msg := range flat {
		ids = append(ids, msg.Id)
	}
	return ids, nil
}

func flatten(threads []Message) []Message {
	var _flatten func(messages []Message, message Message) []Message
	_flatten = func(messages []Message, message Message) []Message {
		messages = append(messages, message)
		for _, child := range message.Children {
			messages = _flatten(messages, child)
		}
		return messages
	}

	flat := make([]Message, 0)
	for _, thread := range threads {
		flat = _flatten(flat, thread)
	}
	return flat
}

func aggregate(flat []Message, threads []Message) []Message {
	for _, thread := range threads {
		message := thread
		children := thread.Children
		message.Children = nil

		flat = append(flat, message)
		flat = aggregate(flat, children)
	}
	return flat
}

func (nm *NotmuchMailstore) getMessage(mid string) (Message, error) {
	threads, err := nm.threads("id:" + mid)
	if err != nil {
		return Message{}, err
	}
	flat := flatten(threads)

	tags := make(map[string]struct{})
	var message Message
	for _, msg := range flat {
		for _, tag := range msg.Tags {
			tags[tag] = struct{}{}
		}
		if msg.Id == mid {
			message = msg
		}
	}

	// Fakely add tags as if they were part of the message
	newTags := make([]string, 0, len(tags))
	for tag := range tags {
		newTags = append(newTags, tag)
	}
	message.Tags = newTags
	return message, nil
}

func (nm *NotmuchMailstore) threads(query string) ([]Message, error) {
	var threads []Message

	var ok bool
	nm.cache.RLock()
	threads, ok = nm.threadsCache[query]
	nm.cache.RUnlock()
	if ok {
		return threads, nil
	}

	var tids []string
	err := nm.json(&tids, "search", "--format=json", "--output=threads", "--sort=oldest-first", query)
	if err != nil {
		return nil, err
	}

	threads = make([]Message, 0)
	if len(tids) > 1 {
		log.Println(len(tids), "threads for query", fmt.Sprintf("%q", query))
	}

	for _, tid := range tids {
		var result []interface{}
		err = nm.json(&result, "show", "--format=json", "--body=false", "--", "thread:"+tid)
		if err != nil {
			return nil, err
		}
		for _, thread := range result {
			topLevelMessages := thread.([]interface{})
			var threadRoot Message
			tags := make([]string, 0)
			threadRoot, tags = newMessage(topLevelMessages[0], tags)
			if len(topLevelMessages) > 1 {
				for _, topLevelMessage := range topLevelMessages[1:] {
					var directChild Message
					directChild, tags = newMessage(topLevelMessage, tags)
					threadRoot.Children = append(threadRoot.Children, directChild)
				}
			}
			threads = append(threads, threadRoot)

			/*
				// Eliminate duplicate in tags
				sort.Strings(tags)
				tagsMap := make(map[string]struct{})
				for _, tag := range tags {
					tagsMap[tag] = struct{}{}
					if _, ok := nm.allMessages[tag]; !ok {
						nm.allMessages[tag] = make(map[string]Message)
					}
				}

				// Flatten list of messages
				flat := make([]Message, 0)
				var flatten func(messages []Message, message Message) []Message
				flatten = func(messages []Message, message Message) []Message {
					messages = append(messages, message)
					for _, child := range message.Children {
						messages = flatten(messages, child)
					}
					return messages
				}
				flat = flatten(flat, threadRoot)

				// Assign messages to all tags
				for _, message := range flat {
					for tag := range tagsMap {
						nm.allMessages[tag][message.Id] = message
					}
				}
			*/
		}
	}

	// Only cache big queries, such as tag-wide or database-wide
	if !strings.Contains(query, " ") && (strings.HasPrefix(query, "tag:") || query == "*") {
		nm.cache.Lock()
		if nm.threadsCache == nil {
			nm.threadsCache = make(map[string][]Message)
		}
		nm.threadsCache[query] = threads
		nm.cache.Unlock()
	}

	return threads, nil
}

func newMessage(raw interface{}, allTags []string) (Message, []string) {
	messageAndChildren := raw.([]interface{})
	message := messageAndChildren[0].(map[string]interface{})
	children := messageAndChildren[1].([]interface{})

	tags := message["tags"].([]interface{})

	msg := Message{
		Id:       message["id"].(string),
		Tags:     make([]string, 0, len(tags)),
		Children: make([]Message, 0, len(children)),
	}
	for _, tag := range tags {
		msg.Tags = append(msg.Tags, tag.(string))
	}

	headers := message["headers"].(map[string]interface{})
	maybe := func(raw interface{}) string {
		if str, ok := raw.(string); ok {
			return str
		}
		return ""
	}
	msg.Header = MessageHeader{
		Subject: maybe(headers["Subject"]),
		From:    maybe(headers["From"]),
		To:      maybe(headers["To"]),
		Cc:      maybe(headers["Cc"]),
		Bcc:     maybe(headers["Bcc"]),
		ReplyTo: maybe(headers["Reply-To"]),
		Date:    maybe(headers["Date"]),
	}

	for _, childRaw := range children {
		var child Message
		child, allTags = newMessage(childRaw, allTags)
		msg.Children = append(msg.Children, child)
	}
	allTags = append(allTags, msg.Tags...)
	return msg, allTags
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

		parts := []string{quoteOrNil(addr.Name), "NIL", quoteOrNil(addrParts[0]), quoteOrNil(addrParts[1])}
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

	// Used for clearing cache
	nm *NotmuchMailstore
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

	c.nm.cache.Lock()
	c.nm.uidToMidMap = nil
	c.nm.midToUidMap = nil
	c.nm.threadsCache = nil
	c.nm.cache.Unlock()
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
		nm:          nm,
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
	io.Copy(ioutil.Discard, c.Reader)
	err := c.cmd.Wait()
	if err != nil {
		log.Println("Error closing", c.args, ": ", err)
	}
	c.l.RUnlock()
	return err
}

func (nm *NotmuchMailstore) json(out interface{}, args ...string) error {
	cmd, err := nm.raw(args...)
	defer cmd.Close()
	if err != nil {
		return err
	}
	return json.NewDecoder(cmd).Decode(&out)
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

type byDate []Message

func (b byDate) Len() int { return len(b) }
func (b byDate) Less(i, j int) bool {
	left, _ := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", b[i].Header.Date)
	right, _ := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", b[j].Header.Date)
	return left.Before(right)
}
func (b byDate) Swap(i, j int) { b[i], b[j] = b[j], b[i] }
