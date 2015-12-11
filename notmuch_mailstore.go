package unpeu

import (
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
	rd, err := nm.raw("count", "tag:"+string(mbox))
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
	count, err := nm.TotalMessages(mbox)
	return count + 1, err
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
