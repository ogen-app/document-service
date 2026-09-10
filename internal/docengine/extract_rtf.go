package docengine

import (
	"strconv"
	"strings"
)

// rtfSkipDestinations name control-word groups whose contents are metadata, not
// body text, and are dropped wholesale.
var rtfSkipDestinations = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true,
	"pict": true, "object": true, "themedata": true, "colorschememapping": true,
	"latentstyles": true, "datastore": true, "generator": true, "header": true,
	"footer": true, "headerl": true, "headerr": true, "headerf": true,
	"footerl": true, "footerr": true, "footerf": true,
}

// extractRtf converts RTF to plain text with a minimal control-word tokenizer
// (no external dependency, matching the pure-Go policy), then splits into
// paragraph blocks. It strips control words, skips destination groups (font/color
// tables etc.), decodes \'hh and \uN, and maps \par/\line to newlines.
func extractRtf(data []byte) ([]Block, error) {
	text := rtfToText(string(data))
	var blocks []Block
	for _, p := range splitParagraphs(text) {
		blocks = append(blocks, Block{Kind: BlockParagraph, Text: validUTF8(p), Anchor: Anchor{Kind: AnchorSection}})
	}
	return blocks, nil
}

type rtfFrame struct {
	ignore     bool
	firstToken bool // next control word may name a destination to skip
}

func rtfToText(s string) string {
	var out strings.Builder
	stack := []rtfFrame{{}}
	top := func() *rtfFrame { return &stack[len(stack)-1] }

	i, n := 0, len(s)
	for i < n {
		switch c := s[i]; c {
		case '{':
			stack = append(stack, rtfFrame{ignore: top().ignore, firstToken: true})
			i++
		case '}':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			i++
		case '\\':
			if i+1 >= n {
				i++
				continue
			}
			i = rtfControl(&out, top(), s, i, n)
		case '\r', '\n':
			i++ // raw line breaks aren't content in RTF
		default:
			if !top().ignore {
				out.WriteByte(c)
			}
			top().firstToken = false
			i++
		}
	}
	return out.String()
}

// rtfControl handles the token starting at s[i]=='\\' and returns the next index.
func rtfControl(out *strings.Builder, fr *rtfFrame, s string, i, n int) int {
	switch next := s[i+1]; next {
	case '\\', '{', '}':
		if !fr.ignore {
			out.WriteByte(next)
		}
		fr.firstToken = false
		return i + 2
	case '*':
		fr.ignore = true // ignorable destination
		return i + 2
	case '\'':
		if i+3 < n {
			if !fr.ignore {
				out.WriteRune(rune(hexByte(s[i+2], s[i+3])))
			}
			fr.firstToken = false
			return i + 4
		}
		return n
	default:
		// Control word: letters, then an optional signed numeric parameter, then an
		// optional single trailing space delimiter.
		j := i + 1
		for j < n && isASCIILetter(s[j]) {
			j++
		}
		word := s[i+1 : j]
		k := j
		if k < n && s[k] == '-' {
			k++
		}
		for k < n && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		param := s[j:k]
		if k < n && s[k] == ' ' {
			k++
		}
		if fr.firstToken && rtfSkipDestinations[word] {
			fr.ignore = true
		}
		fr.firstToken = false
		if !fr.ignore {
			switch word {
			case "par", "line", "sect", "pard":
				out.WriteByte('\n')
			case "tab":
				out.WriteByte('\t')
			case "u":
				if cp, err := strconv.Atoi(param); err == nil {
					if cp < 0 {
						cp += 65536
					}
					out.WriteRune(rune(cp))
				}
			}
		}
		return k
	}
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func hexByte(a, b byte) byte {
	return hexNibble(a)<<4 | hexNibble(b)
}

func hexNibble(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10
	}
	return 0
}
