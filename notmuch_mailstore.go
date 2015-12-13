package unpeu

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vova616/xxhash"
)

var mailboxToNotmuchMapping = map[string]string{
	"INBOX":      "inbox",
	"\\Flagged":  "starred",
	"\\Deleted":  "deleted",
	"\\Draft":    "draft",
	"\\Answered": "answered",
}

var _ Mailstore = &NotmuchMailstore{}

type NotmuchMailstore struct {
	l sync.Mutex
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
	// messages in this box + 1. We'd need some external storage, which we
	// don't want to do... Fortunately the RFC allows us to not predict a
	// UIDNEXT.
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
	cmd, err := nm.raw(args...)
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

func (nm *NotmuchMailstore) Search(mailbox Id, args []searchArgument, returnUid bool) (sequenceSet string, err error) {
	notmuchQuery := parseSearchArguments(args)
	// Remove top-level parenthesis
	notmuchQuery = notmuchQuery[1 : len(notmuchQuery)-1]
	notmuchQuery += "tag:" + string(mailbox)
	log.Printf("notmuch query: %q\n", notmuchQuery)
	rd, err := nm.raw("search", "--format=json", "--output=messages", "--sort=oldest-first", notmuchQuery)
	if err != nil {
		return "", err
	}

	var ids []string
	err = json.NewDecoder(rd).Decode(&ids)
	if err != nil {
		return "", err
	}
	rd.Close()

	if !returnUid {
		return "", fmt.Errorf("Can't output sequence id, we only understand uids")
	}
	for i := range ids {
		// Truncate the sha256 of the message id to 4 bytes and convert it
		// to an uint32. Hopefully there should be no collision. To further
		// reduce chances of collision we could key this with the mailbox
		// name (since RFC says that UID are unique to a mailbox only)
		sum := sha256.Sum256([]byte(ids[i]))
		intId := binary.BigEndian.Uint32(sum[:4])
		ids[i] = strconv.Itoa(int(intId))
	}
	return strings.Join(ids, ","), nil
}

var tagMap = map[string]string{
	"ANSWERED": "tag:answered",
	"DELETED":  "tag:deleted",
	"FLAGGED":  "tag:starred",
	"SEEN":     "-tag:unread",

	"UNSANSWERED": "-tag:answered",
	"UNDELETED":   "-tag:deleted",
	"UNFLAGGED":   "-tag:starred",
	"UNSEEN":      "tag:unread",
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
			query = append(query, tagMap[arg.key])
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
			query = append(query, `subject:"`+arg.values[0]+`"`)
		case "BODY", "TEXT": // Technically wrong, but matches in most interesting cases
			query = append(query, `"`+arg.values[0]+`"`)

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

// ---------------------------
//      Notmuch wrapper
// ---------------------------

// A wrapper around a shell command that implements io.Read, io.Write and
// io.Close.
// io.Read will read from the command's stdoutpipe and io.Write will
// write to the command's stdinpipe. io.Close will close the command,
// properly closing all resources. A notmuchCommand  MUST be closed,
// otherwise resources are leaked
type notmuchCommand struct {
	l    *sync.Mutex
	args []string
	cmd  *exec.Cmd
	io.Reader
	io.WriteCloser
}

func (c notmuchCommand) Close() error {
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

func (nm *NotmuchMailstore) raw(args ...string) (notmuchCommand, error) {
	nm.l.Lock()
	cmd := exec.Command(
		"notmuch",
		args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return notmuchCommand{}, err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return notmuchCommand{}, err
	}
	err = cmd.Start()
	if err != nil {
		return notmuchCommand{}, err
	}

	return notmuchCommand{
		l:           &nm.l,
		args:        args,
		cmd:         cmd,
		Reader:      out,
		WriteCloser: in,
	}, nil
}
