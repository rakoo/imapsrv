package unpeu

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/vova616/xxhash"
)

var _ Mailstore = NotmuchMailstore{}

type NotmuchMailstore struct{}

func (nm NotmuchMailstore) GetMailbox(path []string) (*Mailbox, error) {
	// Get UUID
	rd, err := notmuch("count", "--lastmod")
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
		UidValidity: uidValidity,
	}, nil
}

func (nm NotmuchMailstore) GetMailboxes(path []string) ([]*Mailbox, error) {
	if len(path) > 0 {
		return nil, nil
	}
	rd, err := notmuch("search", "--output=tags", "--format=json", "*")
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
		mailboxes = append(mailboxes, &Mailbox{
			Name: mb,
			Path: []string{mb},
			Id:   Id(mb),
		})
	}
	return mailboxes, nil
}

func (nm NotmuchMailstore) FirstUnseen(mbox Id) (int64, error) {
	// RFC says it's ok to not return first unseed, client should get what
	// it wants through a SEARCH
	return 0, nil
}

func (nm NotmuchMailstore) CountUnseen(mbox Id) (int64, error) {
	rd, err := notmuch("count", "tag:"+string(mbox), "AND", "tag:unread")
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

func (nm NotmuchMailstore) TotalMessages(mbox Id) (int64, error) {
	rd, err := notmuch("count", "tag:"+string(mbox))
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

func (nm NotmuchMailstore) RecentMessages(mbox Id) (int64, error) {
	// Useless
	return 0, nil
}

func (nm NotmuchMailstore) NextUid(mbox Id) (int64, error) {
	count, err := nm.TotalMessages(mbox)
	return count + 1, err
}

// A wrapper around a shell command that implements io.Read and
// io.Close.
// io.Read will read from the command's stdoutpipe while io.Close will
// close the command, properly closing all resources. The Closer MUST be
// closed, otherwise resources are leaked
type notmuchCommand struct {
	cmd *exec.Cmd
	io.ReadCloser
}

func (c notmuchCommand) Close() error {
	return c.cmd.Wait()
}

func notmuch(args ...string) (io.ReadCloser, error) {
	cmd := exec.Command(
		"notmuch",
		args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	return notmuchCommand{cmd, out}, nil
}
