package unpeu

import (
	"bufio"
	"strings"
	"testing"
)

func TestQstring(t *testing.T) {

	r := bufio.NewReader(strings.NewReader("quoted string\"\n"))
	l := createLexer(r)
	l.newLine()
	tk, err := l.qstring()
	if err != nil {
		t.Fail()
	}

	if tk != "quoted string" {
		t.Fail()
	}

}

func TestEmptyLiteral(t *testing.T) {

	r := bufio.NewReader(strings.NewReader("0}\n\n"))
	l := createLexer(r)
	l.newLine()
	tk, err := l.literal()
	if err == nil {
		t.Fail()
	}

	if tk != "" {
		t.Fail()
	}

}

// TestAstring checks the lexer will return a valid <astring> per the
// ABNF rule, or return an error on a failing test
//
// Astring = 1*ASTRING-CHAR / string
//     ASTRING-CHAR = ATOM-CHAR / resp-specials
//         ATOM-CHAR = <any CHAR except atom-specials>
//             atom-specials = "(" / ")" / "{" / SP / CTL / list-wildcards / quoted-specials / resp-specials
//                 list-wildcards = "%" / "*"
//                 quoted-specials = DQUOTE / "\"
//                 resp-specials   = "]"
//     string = quoted / literal
//         quoted = DQUOTE *QUOTED-CHAR DQUOTE
//             QUOTED-CHAR = <any TEXT-CHAR except quoted-specials> / "\" quoted-specials
//                 TEXT-CHAR = <any CHAR except CR and LF>
//                 quoted-specials = DQUOTE / "\"
//         literal = "{" number "}" CRLF *CHAR8 ; number represents the number of CHAR8s
//
// SP  = %x20
// CTL = %x00-1F / %x7F ; controls
// DQUOTE = %x22
// CR  = %x0D
// LF  = %x0A
func TestAstring(t *testing.T) {

	// Test cases receive a map of OUTPUT => INPUT
	passing := map[string]string{
		"a":     "a\r\n",     // 1*ASTRING-CHAR - single
		"this":  "this\r\n",  // 1*ASTRING-CHAR - many
		"burb":  "burb)\r\n", // 1*ASTRING-CHAR - stop at )
		"":      "\"\"\r\n",  // <quoted> with no *QUOTED-CHAR
		"[":     "[\r\n",
		" abcd": "{5}\r\n abcd\n", // <string> alternative <literal>
		"]":     "]\n",            // <ASTRING-CHAR> under the <resp-specials> alternative
	}

	// The failing test case map key is largely irrelevant as they should panic, just included for consistency
	failing := map[string]string{
		" ": " ", // SP
		//"":   "",   // 1*ASTRING-CHAR should have at least one char // TODO : Gets EOF -- should panic?
		"\\": "\\", // <quoted-specials> not allowed in ATOM-CHAR
		//"\"": "\"", // DQUOTE // TODO : Gets EOF -- should panic?
		"%": "%", // <list-wildcard>
		"*": "*", // <list-wildcard>
		")": ")", // <atom-specials> not allowed in ATOM-CHAR
		"(": "(", // <atom-specials> not allowed in ATOM-CHAR
	}

	testAstring := func(in, out string) (bool, string) {
		r := bufio.NewReader(strings.NewReader(in))
		l := createLexer(r)
		err := l.newLine()
		if err != nil {

		}
		ok, tk := l.astring()

		return ok && tk == out, tk
	}

	for o, i := range passing {
		ok, actual := testAstring(i, o)
		if !ok {
			t.Logf("Failed on passing case: input %q, expected output %q, actual output %q",
				i, o, actual)
			t.Fail()
		}
	}

	for o, i := range failing {
		ok, _ := testAstring(i, o)
		if ok {
			// This should not be reached as all failing test cases should trigger a panic
			t.Logf("Failed on failing case: input %q, output %q", i, o)
			t.Fail()
		}
	}

}

func TestSkipSpace(t *testing.T) {

	r := bufio.NewReader(strings.NewReader("abc one\n"))
	l := createLexer(r)
	l.newLine()

	l.astring()
	l.skipSpace()

	// skips past the space
	if l.current() != byte('o') {
		t.Fail()
	}

}

func TestConsume(t *testing.T) {

	r := bufio.NewReader(strings.NewReader("abc\none"))
	l := createLexer(r)
	l.newLine()
	l.consume()

	if l.current() != byte('b') {
		t.Fail()
	}

}

func TestLexesAstring(t *testing.T) {

	r := bufio.NewReader(strings.NewReader("a0001)\n"))
	l := createLexer(r)
	l.newLine()
	ok, token := l.astring()

	if !ok {
		t.Fail()
	}

	if token != "a0001" {
		t.Fail()
	}

}

func TestLexesQuotedString(t *testing.T) {

	r := bufio.NewReader(strings.NewReader("\"A12312\"\n"))
	l := createLexer(r)
	l.newLine()
	ok, token := l.astring()

	if !ok {
		t.Fail()
	}

	if token != "A12312" {
		t.Fail()
	}

}

func TestLexesLiteral(t *testing.T) {

	r := bufio.NewReader(strings.NewReader("{11}\nFRED FOOBAR {7}\n"))
	l := createLexer(r)
	l.newLine()
	ok, token := l.astring()

	if !ok {
		t.Fail()
	}

	// the token after {11} should be of length 11
	if 11 != len(token) {
		t.Fail()
	}

	if "FRED FOOBAR" != token {
		t.Fail()
	}

}

func TestLexesList(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("(ELEM1 (SUB1) ELEM2)"))
	l := createLexer(r)
	l.newLine()
	ok, elements := l.listStrings()

	if !ok {
		t.Fatal("Couldn't lex strings")
	}

	if len(elements) != 3 {
		t.Fatalf("Invalid number of elements, got %v, expected %v", elements, []element{
			element{"ELEM1", nil},
			element{"", []element{element{"SUB1", nil}}},
			element{"ELEM2", nil},
		})
	}

	if elements[0].stringValue != "ELEM1" || elements[0].children != nil {
		t.Fatal("got", elements[0], "expected", element{"ELEM1", nil})
	}

	if elements[1].stringValue != "" ||
		len(elements[1].children) != 1 ||
		elements[1].children[0].stringValue != "SUB1" ||
		elements[1].children[0].children != nil {
		t.Fatal("got", elements[1], "expected", element{"", []element{element{"SUB", nil}}})
	}

	if elements[2].stringValue != "ELEM2" || elements[2].children != nil {
		t.Fatal("got", elements[2], "expected", element{"ELEM2", nil})
	}
}

func TestDoesntLexInvalidList(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(" "))
	l := createLexer(r)
	l.newLine()
	ok, elements := l.listStrings()
	if ok || elements != nil {
		t.Fatal("Invalid empty shouldn't be lexed")
	}

	r = bufio.NewReader(strings.NewReader("A B"))
	l = createLexer(r)
	l.newLine()
	ok, elements = l.listStrings()
	if ok || elements != nil {
		t.Fatal("Invalid non-parenthetized list shouldn't be lexed")
	}
}

func TestReadsLiteralAndRest(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("{3}\r\nab\nabc"))
	l := createLexer(r)
	l.newLine()
	ok, str := l.astring()
	if !ok {
		t.Fatal("Error in reading literal")
	}
	if str != "ab\n" {
		t.Fatalf("Invalid literal, got %s, expected %q", str, "ab\n")
	}

	ok, str = l.astring()
	if !ok {
		t.Fatal("Error in reading string")
	}
	if str != "abc" {
		t.Fatalf("Error in reading string, got %s, expected %q", str, "abc")
	}
}

func TestFailOnInvalidSearchArguments(t *testing.T) {
	failingInputs := []string{
		"BORKED {3}",
		"KEYWORD \\Deleted", // No backslash is allowed in a keyword
		"SMALLER INVALID",   // Must be an integer
		"BEFORE INVALID",    // Must be a date
		"HEADER KEYONLY ",   // Must have a value, even if empty ("")
		"(MISSING CLOSE",
	}

	for _, input := range failingInputs {
		_, err := aggregateSearchArguments([]byte(input))
		if err == nil {
			t.Log("Should have died but didn't")
			t.Fatalf("Input is %q\n", input)

		}
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

func TestInvalidFetchArguments(t *testing.T) {
	inputs := []string{
		"1: ALL",                               // Invalid sequence set
		"ALL",                                  // Missing sequence set
		"10 DELETED",                           // DELETED is not a valid fetch argument
		"10 MIME",                              // MIME can't be at the top level
		"10 BODY.PEEK[HEADER (DATE)]",          // Fields with an invalid section
		"10 BODY[HEADER.FIELDS]",               // Missing fields
		"10 FAST ENVELOPE",                     // Multiple arguments should be inside parenthesis
		"10 BODY[1.4.HEADER.FIELDS DATE]",      // Missing parenthesis for fields
		"10 BODY[1.4.HEADER.FIELDS DATE FROM]", // Missing parenthesis for fields

		// Malformed part
		"10 BODY[]<",
		"10 BODY[]>",
		"10 BODY[]<1.>",
		"10 BODY[]<.1>",
		"10 BODY[]<1,1>",
	}

	for _, input := range inputs {
		r := bufio.NewReader(strings.NewReader(input))
		l := createLexer(r)
		l.newLine()
		ss, args, err := l.fetchArguments()
		if err == nil {
			t.Logf("Should have failed for input %q\n", input)
			t.Fatalf("SequenceSet is %q, arguments is %q\n", ss, args)
		}
	}
}

func TestFetchArguments(t *testing.T) {
	type vector struct {
		input       string
		sequenceSet string
		output      []fetchArgument
	}

	/*
		type fetchArgument struct {
			text    string
			section string
			// Only if text == "HEADER.FIELDS" or text == "HEADER.FIELDS.NOT"
			fields []string
			part   []int
			offset int
			length int
		}
	*/

	vectors := []vector{
		{"10 INTERNALDATE", "10", []fetchArgument{{text: "INTERNALDATE"}}},
		{"10 (ENVELOPE BODYSTRUCTURE)", "10", []fetchArgument{{text: "ENVELOPE"}, {text: "BODYSTRUCTURE"}}},
		{"10:20 (FLAGS)", "10:20", []fetchArgument{{text: "FLAGS"}}},
		{"1,2 BODY.PEEK[]", "1,2", []fetchArgument{{text: "BODY.PEEK", offset: -1}}},
		{"10 BODY[1.4.HEADER.FIELDS (DATE FROM)]<10.28>", "10", []fetchArgument{
			{
				text:    "BODY",
				section: "HEADER.FIELDS",
				fields:  []string{"DATE", "FROM"},
				part:    []int{1, 4},
				offset:  10,
				length:  28,
			},
		}},
		{"10 BODY.PEEK[12.24.176.MIME]<100>", "10", []fetchArgument{
			{
				text:    "BODY.PEEK",
				section: "MIME",
				part:    []int{12, 24, 176},
				offset:  100,
			},
		}},
		{"10 BODY.PEEK[2.1.4.HEADER.FIELDS.NOT (SUBJECT)]<100>", "10", []fetchArgument{
			{
				text:    "BODY.PEEK",
				section: "HEADER.FIELDS.NOT",
				fields:  []string{"SUBJECT"},
				part:    []int{2, 1, 4},
				offset:  100,
			},
		}},

		// Resolution of macro
		{"10 ALL", "10", []fetchArgument{{text: "FLAGS"}, {text: "INTERNALDATE"}, {text: "RFC822.SIZE"}, {text: "ENVELOPE"}}},

		{"10 BODY[1]", "10", []fetchArgument{{text: "BODY", offset: -1, part: []int{1}}}},
	}

	compareFetchArgument := func(actual, expected fetchArgument) bool {
		if actual.text != expected.text ||
			actual.section != expected.section ||
			actual.offset != expected.offset ||
			actual.length != expected.length {
			return false
		}
		if len(actual.fields) != len(expected.fields) {
			return false
		}
		for i, act := range actual.fields {
			if act != expected.fields[i] {
				return false
			}
		}
		if len(actual.part) != len(expected.part) {
			return false
		}
		for i, act := range actual.part {
			if act != expected.part[i] {
				return false
			}
		}
		return true
	}

	for _, v := range vectors {
		r := bufio.NewReader(strings.NewReader(v.input))
		l := createLexer(r)
		l.newLine()
		ss, args, err := l.fetchArguments()
		if err != nil {
			t.Logf("Error parsing fetch arguments: %q\n", v.input)
			t.Fatal(err)
		}
		if ss != v.sequenceSet {
			t.Log("Different sequence sets for %q\n", v.input)
			t.Logf("Got      %v\n", ss)
			t.Logf("Expected %v\n", v.sequenceSet)
			t.FailNow()
		}
		if len(args) != len(v.output) {
			t.Fatalf("Invalid parsing for %q\n", v.input)
		}
		for i, actual := range args {
			if !compareFetchArgument(actual, v.output[i]) {
				t.Log("Different outputs for %q\n", v.input)
				t.Logf("Got      %v\n", actual)
				t.Logf("Expected %v\n", v.output)
				t.FailNow()
			}
		}
	}
}
