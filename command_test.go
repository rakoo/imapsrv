package unpeu

import (
	"testing"
	"time"
)
import "fmt"

func setupTest() (*Server, *session) {
	m := &TestMailstore{}
	s := NewServer(
		StoreOption(m),
	)
	//s.Start()
	sess := createSession("1", s.config, s, nil, nil) // TODO: listener and net.Conn
	return s, sess
}

// TestMailstore is a dummy mailstore
type TestMailstore struct {
}

// GetMailbox gets dummy Mailbox information
func (m *TestMailstore) GetMailbox(path []string) (*Mailbox, error) {
	return &Mailbox{
		Name: "inbox",
		Id:   "1",
	}, nil
}

// GetMailboxes lists dummy Mailboxes
func (m *TestMailstore) GetMailboxes(path []string) ([]*Mailbox, error) {
	if len(path) == 0 {
		// Root
		return []*Mailbox{
			{
				Name: "inbox",
				Path: []string{"inbox"},
				Id:   "1",
			},
			{
				Name: "spam",
				Path: []string{"spam"},
				Id:   "2",
			},
		}, nil
	} else if len(path) == 1 && path[0] == "inbox" {
		return []*Mailbox{
			{
				Name: "starred",
				Path: []string{"inbox", "stared"},
				Id:   "3",
			},
		}, nil
	} else {
		return []*Mailbox{}, nil
	}
}

// FirstUnseen gets a dummy number of first unseen messages in an IMAP mailbox
func (m *TestMailstore) FirstUnseen(mbox Id) (int64, error) {
	return 4, nil
}

// TotalMessages gets a dummy number of messages in an IMAP mailbox
func (m *TestMailstore) TotalMessages(mbox Id) (int64, error) {
	return 8, nil
}

// RecentMessages gets a dummy number of unread messages in an IMAP mailbox
func (m *TestMailstore) RecentMessages(mbox Id) (int64, error) {
	return 4, nil
}

// NextUid gets a dummy next-uid in an IMAP mailbox
func (m *TestMailstore) NextUid(mbox Id) (int64, error) {
	return 9, nil
}

// CountUnseen gets a dummy next-uid in an IMAP mailbox
func (m *TestMailstore) CountUnseen(mbox Id) (int64, error) {
	return 9, nil
}

// AppendMessage appends the message to an IMAP mailbox
func (m *TestMailstore) AppendMessage(mailbox string, flags []string, dateTime time.Time, message string) error {
	return nil
}

// Search searches messages in an IMAP mailbox
// The output sequenceSet doesn't contain any '*'
func (m *TestMailstore) Search(mbox Id, args []searchArgument, returnUid, returnThreads bool) (ids []threadMember, err error) {
	return nil, nil
}

// Fetch fetches information on the selected messages in the given
// mailbox.
// The output is a list of list. The first level has one element by
// message, the second level has one element per desired field in the message
func (m *TestMailstore) Fetch(mailbox Id, sequenceSet string, args []fetchArgument, returnUid bool) ([]messageFetchResponse, error) {
	return nil, nil
}

func (m *TestMailstore) Flag(mode flagMode, mbox Id, sequenceSet string, useUids bool, flags []string) ([]messageFetchResponse, error) {
	return nil, nil
}

// TestCapabilityCommand tests the correctness of the CAPABILITY command
func _TestCapabilityCommand(t *testing.T) {
	_, session := setupTest()
	cap := &capability{tag: "A00001"}
	resp := cap.execute(session)
	// TODO: STARTTLS shouldn't always be available? (i.e. after using STARTTLS)
	if (resp.tag != "A00001") || (resp.message != "CAPABILITY completed") || (resp.untagged[0] != "CAPABILITY IMAP4rev1 STARTTLS") {
		t.Error("Capability Failed - unexpected response.")
		fmt.Println(resp)
	}
}

// TestLogoutCommand tests the correctness of the LOGOUT command
func _TestLogoutCommand(t *testing.T) {
	_, session := setupTest()
	log := &logout{tag: "A00004"}
	resp := log.execute(session)
	if (resp.tag != "A00004") || (resp.message != "LOGOUT completed") || (resp.untagged[0] != "BYE IMAP4rev1 Server logging out") {
		t.Error("Logout Failed - unexpected response.")
		fmt.Println(resp)
	}
}

func TestSearch(t *testing.T) {

	type vector struct {
		input  string
		output []searchArgument
	}

	vectors := []vector{
		{"KEYWORD DELETED", []searchArgument{{key: "KEYWORD", values: []string{"DELETED"}}}},
		{"SMALLER \"1024\"", []searchArgument{{key: "SMALLER", values: []string{"1024"}}}},
		{"SENTON 20-Jan-1830", []searchArgument{{key: "SENTON", values: []string{"20-Jan-1830"}}}},
		{"HEADER KEY \"\"", []searchArgument{{key: "HEADER", values: []string{"KEY", ""}}}},
		{"HEADER KEY VALUE", []searchArgument{{key: "HEADER", values: []string{"KEY", "VALUE"}}}},
		{"ALL ANSWERED", []searchArgument{{key: "ALL"}, {key: "ANSWERED"}}},
		{"TO {7}\r\na@b.com", []searchArgument{{key: "TO", values: []string{"a@b.com"}}}},
		{"(ALL DELETED)", []searchArgument{
			{group: true, children: []searchArgument{{key: "ALL"}, {key: "DELETED"}}},
		}},
		{"(ALL NOT (DELETED (NOT SEEN)))", []searchArgument{
			{
				group: true,
				children: []searchArgument{
					{key: "ALL"},
					{
						not:   true,
						group: true,
						children: []searchArgument{
							{key: "DELETED"},
							{
								group: true,
								children: []searchArgument{{
									not: true,
									key: "SEEN",
								}},
							},
						},
					},
				}},
		}},

		// The OR only applies for ALL and DELETED, not for SEEN
		{"OR ALL DELETED SEEN", []searchArgument{
			{
				or:       true,
				children: []searchArgument{{key: "ALL"}, {key: "DELETED"}},
			},
			{key: "SEEN"},
		}},

		{"DELETED UID 1:3,7,11:13 2,4:*", []searchArgument{
			{key: "DELETED"},
			{key: "UID", values: []string{"1:3,7,11:13"}},
			{key: "SEQUENCESET", values: []string{"2,4:*"}},
		}},

		{"OR DELETED NOT SEEN", []searchArgument{
			{
				or:       true,
				children: []searchArgument{{key: "DELETED"}, {key: "SEEN", not: true}},
			},
		}},

		// Here we test management of depth when we mix an OR and
		// parenthesis
		{`OR DELETED (OR SUBJECT "subject" FROM "a@b.com")`, []searchArgument{
			{
				or: true,
				children: []searchArgument{
					{key: "DELETED"},
					{
						group: true,
						children: []searchArgument{
							{
								or: true,
								children: []searchArgument{
									{key: "SUBJECT", values: []string{"subject"}},
									{key: "FROM", values: []string{"a@b.com"}},
								},
							},
						},
					},
				},
			},
		}},
	}

	for _, v := range vectors {
		actualArgs, err := aggregateSearchArguments([]byte(v.input))
		if err != nil {
			t.Logf("Invalid input: %q", v.input)
			t.Fatal(err)
		}

		var compareSearchArguments func(actual, expected searchArgument) bool
		compareSearchArguments = func(actual, expected searchArgument) bool {
			if actual.key != expected.key ||
				actual.or != expected.or ||
				actual.group != expected.group ||
				len(actual.values) != len(expected.values) {
				return false
			}
			for i, value := range actual.values {
				if value != expected.values[i] {
					return false
				}
			}

			if len(actual.children) != len(expected.children) {
				return false
			}

			for i, child := range actual.children {
				if !compareSearchArguments(child, expected.children[i]) {
					return false
				}
			}

			return true
		}

		if len(actualArgs) != len(v.output) {
			t.Log("Invalid number of elems for", v.input)
			t.Logf("got      %#v\n", actualArgs)
			t.Logf("expected %#v\n", v.output)
			t.FailNow()
		}
		for i, actual := range actualArgs {
			expected := v.output[i]
			if !compareSearchArguments(actual, expected) {
				t.Log("Invalid parsing for", v.input)
				t.Logf("got      %#v\n", actualArgs)
				t.Logf("expected %#v\n", v.output)
				t.FailNow()
			}
		}
	}
}
