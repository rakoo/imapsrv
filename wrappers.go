package imapsrv

import (
	"bufio"
	"bytes"
	"net/mail"
)

// A wrapper around a Mailbox that provides helper functions
// and sequence numbers
type mailboxWrap struct {
	// The user provided mailbox
	provider Mailbox
	// Sequence number to uid mapping
	seqNums []int32
}

// A wrapper around a Message the provides parsing functions
type messageWrap struct {
	// The uid of the message
	uid int32
	// The user provided message
	provider Message
	// A parsed version of the message, nil if the message has not been parsed
	message *mail.Message
	// The mime structure of the message, nil if the message has not been parsed
	mime *enmime.MIMEBody
}

const dateFormat = "02-Jan-2006 15:04:05 -0700"

// Get a mailbox from a mailstore
func getMailbox(store Mailstore, owner string, path []string) (*mailboxWrap, error) {
	mbox, err := store.Mailbox(owner, path)
	if err != nil {
		return nil, err
	}

	return wrapMailbox(mbox), nil
}

// Get mailboxes from a mailstore
func getMailboxes(store Mailstore, owner string, path []string) ([]*mailboxWrap, error) {
	mboxes, err := store.Mailboxes(owner, path)
	if err != nil {
		return nil, err
	}

	ret := make([]*mailboxWrap, len(mboxes))
	for i, mbox := range mboxes {
		ret[i] = wrapMailbox(mbox)
	}

	return ret, nil
}

// Wrap a Mailbox returned by the mailstore
func wrapMailbox(mbox Mailbox) *mailboxWrap {
	return &mailboxWrap{
		provider: mbox,
	}
}

// Fetch the message from the mailbox with the given sequence number
func (m *mailboxWrap) fetch(seqnum int32) (*messageWrap, error) {

	uid, err := m.getUid(seqnum)
	if err != nil {
		return nil, err
	}

	// Get the message
	msg, err := m.provider.Fetch(uid)
	if err != nil {
		return nil, err
	}

	// Wrap and return the message
	return &messageWrap{
		uid:      uid,
		provider: msg}, nil
}

// Get the Uid for the given sequence number
func (m *mailboxWrap) getUid(seqnum int32) (int32, error) {

	// Build the sequence number array
	if m.seqNums == nil {
		uids, err := m.provider.AllUids()
		if err != nil {
			return -1, err
		}

		m.seqNums = make([]int32, len(uids))

		for i, uid := range uids {
			m.seqNums[i] = uid
		}
	}

	// Return the UID
	return m.seqNums[seqnum], nil

}

// Get a mail.Message from a wrapped message
func (m *messageWrap) getMessage() (*mail.Message, error) {

	if m.message == nil {

		reader, err := m.provider.Reader()
		if err != nil {
			return nil, err
		}

		m.message, err = mail.ReadMessage(reader)
		if err != nil {
			return nil, err
		}
	}

	return m.message, nil
}

// Get the mime structure from a wrapped message
func (m *messageWrap) getMime() (*enmime.MIMEBody, error) {

	// Is the mime already available?
	if m.mime == nil {
		msg, err := m.getMessage()
		if err != nil {
			return nil, err
		}

		m.mime, err = enmime.ParseMIMEBody(msg)
		if err != nil {
			return nil, err
		}
	}

	return m.mime, nil
}

// Get the raw header from a message
func (m *messageWrap) rfc822Header() (string, error) {

	reader, err := m.provider.Reader()
	if err != nil {
		return "", err
	}

	// Read the message line by line
	buf := new(bytes.Buffer)
	bufReader := bufio.NewReader(reader)

	for {
		line, err := bufReader.ReadBytes('\n')
		if err != nil {
			return "", err
		}

		// Is this the \r\n that signifies the end of the header?
		if len(line) == 2 {
			break
		}

		buf.Write(line)
	}

	return buf.String(), nil
}

// Get the size of a message
func (m *messageWrap) size() (uint32, error) {
	return m.provider.Size()
}

// Get a formatted internal date from a mail message wrapper
func (m *messageWrap) internalDate() (string, error) {
	date, err := m.provider.InternalDate()
	if err != nil {
		return "", err
	}

	return date.Format(dateFormat), nil
}
