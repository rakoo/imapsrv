package unpeu

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
)

type structureParser func(rd io.Reader, mediaType string, params map[string]string) (part, error)

var parsersByType map[string]structureParser

func init() {
	parsersByType = map[string]structureParser{
		"multipart/":     structureParser(multipartParser),
		"text/":          structureParser(textParser),
		"message/rfc822": structureParser(messageBodystructureParser),
	}
}

type part interface {
	structure() string
}

func parse(rd io.Reader, mediaType string, params map[string]string) (part, error) {
	chosenParser := defaultParser
	for k, v := range parsersByType {
		if strings.HasPrefix(mediaType, k) {
			chosenParser = v
		}
	}
	part, err := chosenParser(rd, mediaType, params)
	return part, err
}

// ----------------
//   Message part
// ----------------

// mediaType and params are not read; the reader is self-sufficient
func messageBodystructureParser(rd io.Reader, mediaType string, params map[string]string) (part, error) {

	type parserAndError struct {
		dp  defaultPart
		err error
	}
	fullRead := make(chan parserAndError, 1)
	pr, pw := io.Pipe()
	go func() {
		p, err := defaultParser(pr, mediaType, params)
		fullRead <- parserAndError{p.(defaultPart), err}
	}()

	var lc lineCounter
	mw := io.MultiWriter(&lc, pw)

	tr := io.TeeReader(rd, mw)

	buf := bufio.NewReader(tr)
	tp := textproto.NewReader(buf)
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}

	messageId := header.Get("Message-Id")
	if messageId[0] == lessThan && messageId[len(messageId)-1] == moreThan {
		messageId = messageId[1 : len(messageId)-1]
	}
	// Technically if a field doesn't exist the corresponding value should
	// be NIL; only if it exists AND is empty should it be set to "".
	envelopeFields := []string{
		quoteOrNil(header.Get("Date")), literalify(header.Get("Subject")),
		addresses(header, "From"), addresses(header, "Sender"), addresses(header, "Reply-To"), addresses(header, "To"), addresses(header, "Cc"), addresses(header, "Bcc"),
		quoteOrNil(header.Get("In-Reply-To")), quoteOrNil(messageId),
	}

	envelope := `(` + strings.Join(envelopeFields, " ") + `)`

	mediaType, params, err = mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	body, err := parse(buf, mediaType, params)
	if err != nil {
		return nil, err
	}

	result := <-fullRead
	result.dp.suffix += fmt.Sprintf(" %s %s %s", envelope, body.structure(), quoteOrNil(strconv.Itoa(lc.numlines)))
	return result.dp, result.err
}

// ----------------
//   Multipart part
// ----------------

var _ part = multipartPart{}

type multipartPart struct {
	subtype string
	parts   []part
}

func (mpp multipartPart) structure() string {
	ret := `(`
	for _, p := range mpp.parts {
		ret += p.structure()
	}
	ret += " " + up(mpp.subtype)
	ret += `)`
	return ret
}

// Parse a multipart reader into a list of parts.
// If there is any error during parsing, it is interrupted and the error
// is returned
func multipartParser(in io.Reader, mediaType string, params map[string]string) (part, error) {
	split := strings.Split(mediaType, "/")
	if len(split) != 2 {
		return multipartPart{}, fmt.Errorf("Invalid mediaType in multipartParser:", mediaType)
	}
	rd := multipart.NewReader(in, params["boundary"])

	mp := multipartPart{
		subtype: split[1],
		parts:   make([]part, 0),
	}
	for {
		p, err := rd.NextPart()
		if err != nil {
			if err != io.EOF {
				return mp, err
			}
			return mp, nil
		}

		mediaType, params, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if err != nil {
			return mp, err
		}
		subPart, err := parse(p, mediaType, params)
		if err != nil {
			return mp, err
		}
		mp.parts = append(mp.parts, subPart)
	}

	return mp, nil
}

// ----------------
//    Text part
// ----------------

func textParser(rd io.Reader, mediaType string, params map[string]string) (part, error) {

	var lc lineCounter
	tr := io.TeeReader(rd, &lc)
	part, err := defaultParser(tr, mediaType, params)

	dp := part.(defaultPart)
	dp.suffix = strconv.Itoa(lc.numlines)
	return dp, err
}

// ----------------
//   Default part
// ----------------

var _ part = defaultPart{}

type defaultPart struct {
	typ         string
	subType     string
	params      map[string]string
	id          string
	description string
	encoding    string
	size        string

	// When used from another parser, this data is added as-is at the end before
	// the closing parenthesis. Don't forget to use empty spaces before
	// it to separate from the other fields
	suffix string
}

func (dp defaultPart) structure() string {

	paramsList := make([]string, 0, len(dp.params))
	for k, v := range dp.params {
		paramsList = append(paramsList, fmt.Sprintf("%s %s", up(k), up(v)))
	}

	fields := []string{
		up(dp.typ),
		up(dp.subType),
		`(` + strings.Join(paramsList, " ") + `)`,
		up(dp.id),
		up(dp.description),
		up(dp.encoding),
		dp.size,
		dp.suffix,
	}
	return `(` + strings.Join(fields, " ") + `)`
}

func defaultParser(rd io.Reader, mediaType string, params map[string]string) (part, error) {
	split := strings.Split(mediaType, "/")
	if len(split) != 2 {
		return defaultPart{}, fmt.Errorf("Invalid mediaType in defaultParser:", mediaType)
	}

	dp := defaultPart{
		typ:     split[0],
		subType: split[1],
		params:  params,
	}
	if id, ok := params["id"]; ok {
		dp.id = id
	}
	if id, ok := params["content-id"]; ok {
		dp.id = id
	}
	if desc, ok := params["description"]; ok {
		dp.description = desc
	}
	if encoding, ok := params["content-transfer-encoding"]; ok {
		dp.encoding = encoding
	}
	if size, ok := params["content-length"]; ok {
		dp.size = size
	}

	// Fetch info from reader
	n, err := io.Copy(ioutil.Discard, rd)
	if err != nil {
		return dp, err
	}
	if dp.size == "" {
		dp.size = strconv.Itoa(int(n))
	}

	return dp, nil
}

// ----------------
//     Helpers
// ----------------

type lineCounter struct {
	numlines int
}

func (lc *lineCounter) Write(p []byte) (n int, err error) {
	c := bytes.Count(p, []byte{'\n'})
	lc.numlines += c
	return len(p), nil
}

func quoteOrNil(in string) string {
	if in == "" {
		return "NIL"
	}
	return `"` + in + `"`
}

func up(in string) string {
	return quoteOrNil(strings.ToUpper(in))
}
