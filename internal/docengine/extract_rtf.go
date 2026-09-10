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
	uc         int  // \ucN: chars of fallback text following a \u to skip (default 1)
}

func rtfToText(s string) string {
	var out strings.Builder
	stack := []rtfFrame{{uc: 1}}
	top := func() *rtfFrame { return &stack[len(stack)-1] }

	// skip counts fallback characters to drop after a \uN so the ANSI substitute
	// (e.g. "?") doesn't appear alongside the decoded Unicode rune.
	skip := 0
	i, n := 0, len(s)
	for i < n {
		switch c := s[i]; c {
		case '{':
			stack = append(stack, rtfFrame{ignore: top().ignore, firstToken: true, uc: top().uc})
			skip = 0
			i++
		case '}':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			skip = 0
			i++
		case '\\':
			if i+1 >= n {
				i++
				continue
			}
			i = rtfControl(&out, top(), s, i, n, &skip)
		case '\r', '\n':
			i++ // raw line breaks aren't content in RTF
		default:
			if skip > 0 {
				skip-- // this is \u fallback text — drop it
				i++
				continue
			}
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
// skip tracks pending \u fallback characters to drop.
func rtfControl(out *strings.Builder, fr *rtfFrame, s string, i, n int, skip *int) int {
	switch next := s[i+1]; next {
	case '\\', '{', '}':
		if *skip > 0 {
			*skip--
			return i + 2
		}
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
			if *skip > 0 {
				*skip-- // \'xx as \u fallback
			} else if !fr.ignore {
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
		switch word {
		case "uc":
			// \ucN sets how many fallback chars follow each subsequent \u.
			if v, err := strconv.Atoi(param); err == nil && v >= 0 {
				fr.uc = v
			}
		case "u":
			if cp, err := strconv.Atoi(param); err == nil {
				if cp < 0 {
					cp += 65536
				}
				if !fr.ignore {
					out.WriteRune(rune(cp))
				}
				*skip = fr.uc // drop the ANSI fallback that follows
			}
		default:
			if !fr.ignore {
				switch word {
				case "par", "line", "sect", "pard":
					out.WriteByte('\n')
				case "tab":
					out.WriteByte('\t')
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
