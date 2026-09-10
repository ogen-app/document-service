package docengine

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// extractEml parses an .eml message into prose blocks. Headers (from/to/subject/
// date) are metadata, not body. For a multipart message it prefers the text/plain
// part, falling back to text/html (via the HTML extractor); attachments are
// ignored in v1. Quoted reply chains and signature blocks are stripped from plain
// text so retrieval isn't polluted by the same quoted thread repeated in every
// reply.
func extractEml(data []byte) ([]Block, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: parse email: %v", ErrInvalid, err)
	}
	body, isHTML := emlBody(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)

	if isHTML {
		if blocks, err := extractHTML([]byte(body)); err == nil && len(blocks) > 0 {
			for i := range blocks {
				blocks[i].Anchor.Kind = AnchorEmail
			}
			return blocks, nil
		}
	}

	var blocks []Block
	for _, p := range splitParagraphs(stripQuoted(body)) {
		blocks = append(blocks, Block{Kind: BlockParagraph, Text: validUTF8(p), Anchor: Anchor{Kind: AnchorEmail}})
	}
	return blocks, nil
}

// emlBody returns the message body text and whether it is HTML, decoding the
// transfer encoding and (for multipart) selecting a text part.
func emlBody(contentType, cte string, r io.Reader) (string, bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		if text, isHTML, ok := multipartText(r, params["boundary"]); ok {
			return text, isHTML
		}
		return "", false
	}
	body, _ := io.ReadAll(io.LimitReader(r, maxZipEntryBytes))
	return decodeTransfer(body, cte), mediaType == "text/html"
}

// multipartText walks the parts, returning the first text/plain (preferred) or
// text/html body. Nested multiparts are searched one level deep.
func multipartText(r io.Reader, boundary string) (string, bool, bool) {
	if boundary == "" {
		return "", false, false
	}
	mr := multipart.NewReader(r, boundary)
	var htmlBody string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		ct := part.Header.Get("Content-Type")
		mediaType, params, _ := mime.ParseMediaType(ct)
		switch {
		case strings.HasPrefix(mediaType, "multipart/"):
			if text, isHTML, ok := multipartText(part, params["boundary"]); ok && !isHTML {
				return text, false, true
			} else if ok && htmlBody == "" {
				htmlBody = text
			}
		case mediaType == "text/plain":
			body, _ := io.ReadAll(io.LimitReader(part, maxZipEntryBytes))
			return decodeTransfer(body, part.Header.Get("Content-Transfer-Encoding")), false, true
		case mediaType == "text/html":
			if htmlBody == "" {
				body, _ := io.ReadAll(io.LimitReader(part, maxZipEntryBytes))
				htmlBody = decodeTransfer(body, part.Header.Get("Content-Transfer-Encoding"))
			}
		}
	}
	if htmlBody != "" {
		return htmlBody, true, true
	}
	return "", false, false
}

// decodeTransfer decodes a body per its Content-Transfer-Encoding.
func decodeTransfer(body []byte, cte string) string {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		if dec, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(body)), "")); err == nil {
			return string(dec)
		}
	case "quoted-printable":
		if dec, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body))); err == nil {
			return string(dec)
		}
	}
	return string(body)
}

// stripQuoted removes quoted reply lines (leading ">") and everything after a
// signature delimiter ("-- ").
func stripQuoted(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln == "-- " {
			break // signature block
		}
		if strings.HasPrefix(strings.TrimSpace(ln), ">") {
			continue // quoted reply
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}
