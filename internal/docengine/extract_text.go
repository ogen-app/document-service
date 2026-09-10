package docengine

import "strings"

// extractText turns plain UTF-8 text into paragraph blocks split on blank lines.
// Charset detection for legacy Windows-125x encodings is a follow-up; v1 assumes
// UTF-8 and sanitises invalid bytes at chunk time.
func extractText(data []byte) []Block {
	text := validUTF8(string(data))
	paras := splitParagraphs(text)
	blocks := make([]Block, 0, len(paras))
	for _, p := range paras {
		blocks = append(blocks, Block{Kind: BlockParagraph, Text: p, Anchor: Anchor{Kind: AnchorSection}})
	}
	return blocks
}

// splitParagraphs breaks text on blank lines, dropping empties.
func splitParagraphs(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
