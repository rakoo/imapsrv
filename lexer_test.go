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

		/*
			// Catch any panics
			defer func() {
				if r := recover(); r != nil {
					// EOFs are easily obscured as they are also a form of panic in the system
					// but do not constitute an 'expected' panic type here
					if r.(parseError).Error() == "EOF" {
						t.Logf("Bad panic on input: %q, output: %q", in, out)
						panic("EOF found in TestAstring - should not be present, correct the test(s)")
					}
				}
			}()
		*/

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
		t.Fatal("Invalid number of elements")
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
		t.Fatal("Invalid list shouldn't be lexed")
	}

	r = bufio.NewReader(strings.NewReader("A B"))
	l = createLexer(r)
	l.newLine()
	ok, elements = l.listStrings()
	if ok || elements != nil {
		t.Fatal("Invalid list shouldn't be lexed")
	}
}
