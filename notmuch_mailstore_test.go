package unpeu

import "testing"

func TestParseSearchArguments(t *testing.T) {
	// We're using the lexer to parse IMAP input. We'll assume the lexer
	// works as needed

	type vector struct {
		input  string
		output string
	}

	vectors := []vector{
		{"SEEN FLAGGED", "(-tag:unread tag:starred)"},
		{"KEYWORD deleted", "(tag:deleted)"},
		{"SEEN", "(-tag:unread)"},
		{"NOT SEEN", "(tag:unread)"},
		{"SENTSINCE 20-Jan-2012", "(date:20-Jan-2012..)"},
		{`BODY "How are you ?"`, `("How are you ?")`},
		{"OR DELETED SEEN", "((tag:deleted) OR (-tag:unread))"},
		{"OR DELETED NOT SEEN", "((tag:deleted) OR (tag:unread))"},
		{`OR DELETED (OR SUBJECT "subject" FROM "a@b.com")`, `((tag:deleted) OR ((subject:"subject") OR (from:a@b.com)))`},
	}

	for _, v := range vectors {
		args, err := aggregateSearchArguments([]byte(v.input))
		if err != nil {
			t.Logf("Invalid input: %q", v.input)
			t.Fatal(err)
		}
		actualOutput := parseSearchArguments(args)

		if v.output != actualOutput {
			t.Log("Invalid parsing of search arguments for", v.input)
			t.Logf("Got      %v\n", actualOutput)
			t.Logf("Expected %v\n", v.output)
			t.FailNow()
		}
	}
}
