package markdown

import (
	"context"
	"sort"
	"strings"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

type Parser struct {
	markdown goldmark.Markdown
}

func NewParser() *Parser {
	return &Parser{markdown: goldmark.New(goldmark.WithExtensions(extension.GFM))}
}

func (p *Parser) Parse(ctx context.Context, source []byte) (domain.ParsedDocument, error) {
	if err := ctx.Err(); err != nil {
		return domain.ParsedDocument{}, err
	}

	root := p.markdown.Parser().Parse(text.NewReader(source))
	starts := lineStarts(source)
	headings := make([]string, 6)
	blocks := make([]domain.MarkdownBlock, 0, root.ChildCount())
	nextSourceLine := 1

	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		if err := ctx.Err(); err != nil {
			return domain.ParsedDocument{}, err
		}
		if heading, ok := node.(*ast.Heading); ok {
			headings[heading.Level-1] = headingText(heading, source)
			clear(headings[heading.Level:])
			_, endLine := sourceRange(node, starts)
			if endLine >= nextSourceLine {
				nextSourceLine = endLine + 1
			}
			continue
		}

		blockType, ok := markdownBlockType(node, source)
		if !ok {
			continue
		}
		startLine, endLine := sourceRange(node, starts)
		if _, ok := node.(*ast.FencedCodeBlock); ok {
			startLine, endLine = fencedCodeSourceRange(source, starts, nextSourceLine, startLine, endLine)
		}
		block := domain.MarkdownBlock{
			Type:        blockType,
			HeadingPath: currentHeadingPath(headings),
			RawContent:  rawLines(source, starts, startLine, endLine),
			Content:     nodeText(node, source),
			StartLine:   startLine,
			EndLine:     endLine,
			Ordinal:     len(blocks),
		}
		blocks = append(blocks, block)
		if endLine >= nextSourceLine {
			nextSourceLine = endLine + 1
		}
	}

	return domain.ParsedDocument{Blocks: blocks}, nil
}

func lineNumber(offset int, starts []int) int {
	if len(starts) == 0 || offset < 0 {
		return 0
	}
	index := sort.Search(len(starts), func(index int) bool {
		return starts[index] > offset
	})
	return index
}

func headingText(node ast.Node, source []byte) string {
	return normalizeWhitespace(nodeText(node, source))
}

func nodeText(node ast.Node, source []byte) string {
	switch typed := node.(type) {
	case *ast.CodeBlock:
		return string(typed.Lines().Value(source))
	case *ast.FencedCodeBlock:
		return string(typed.Lines().Value(source))
	case *ast.HTMLBlock:
		return string(typed.Text(source))
	}

	var builder strings.Builder
	_ = ast.Walk(node, func(current ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch typed := current.(type) {
			case *ast.Text:
				builder.Write(typed.Value(source))
				if typed.SoftLineBreak() || typed.HardLineBreak() {
					builder.WriteByte('\n')
				}
			case *ast.String:
				builder.Write(typed.Value)
			case *ast.AutoLink:
				builder.Write(typed.Label(source))
			}
			return ast.WalkContinue, nil
		}

		switch current.(type) {
		case *ast.Paragraph, *ast.ListItem, *extensionast.TableHeader, *extensionast.TableRow:
			builder.WriteByte('\n')
		case *extensionast.TableCell:
			builder.WriteByte('\t')
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(builder.String())
}

func sourceRange(node ast.Node, starts []int) (int, int) {
	first := -1
	last := -1
	include := func(start, stop int) {
		if start < 0 {
			return
		}
		if first < 0 || start < first {
			first = start
		}
		end := stop - 1
		if end < start {
			end = start
		}
		if end > last {
			last = end
		}
	}

	_ = ast.Walk(node, func(current ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if current.Type() != ast.TypeInline {
			lines := current.Lines()
			for index := 0; index < lines.Len(); index++ {
				segment := lines.At(index)
				include(segment.Start, segment.Stop)
			}
		}
		switch typed := current.(type) {
		case *ast.Text:
			include(typed.Segment.Start, typed.Segment.Stop)
		case *ast.RawHTML:
			for index := 0; index < typed.Segments.Len(); index++ {
				segment := typed.Segments.At(index)
				include(segment.Start, segment.Stop)
			}
		case *ast.HTMLBlock:
			if typed.HasClosure() {
				include(typed.ClosureLine.Start, typed.ClosureLine.Stop)
			}
		}
		return ast.WalkContinue, nil
	})
	if first < 0 {
		first = node.Pos()
		last = first
	}
	return lineNumber(first, starts), lineNumber(last, starts)
}

func markdownBlockType(node ast.Node, source []byte) (domain.BlockType, bool) {
	switch typed := node.(type) {
	case *ast.Paragraph:
		if paragraphContainsOnly(typed, ast.KindImage, source) {
			return domain.BlockImage, true
		}
		if paragraphContainsOnly(typed, ast.KindRawHTML, source) {
			return domain.BlockRawHTML, true
		}
		return domain.BlockParagraph, true
	case *ast.List:
		return domain.BlockList, true
	case *extensionast.Table:
		return domain.BlockTable, true
	case *ast.Blockquote:
		return domain.BlockQuote, true
	case *ast.CodeBlock, *ast.FencedCodeBlock:
		return domain.BlockCode, true
	case *ast.HTMLBlock:
		return domain.BlockRawHTML, true
	case *ast.ThematicBreak:
		return domain.BlockThematicBreak, true
	default:
		return "", false
	}
}

func paragraphContainsOnly(paragraph *ast.Paragraph, kind ast.NodeKind, source []byte) bool {
	found := false
	for child := paragraph.FirstChild(); child != nil; child = child.NextSibling() {
		if child.Kind() == kind {
			found = true
			continue
		}
		if textNode, ok := child.(*ast.Text); ok && strings.TrimSpace(string(textNode.Value(source))) == "" {
			continue
		}
		return false
	}
	return found
}

func currentHeadingPath(headings []string) []string {
	path := make([]string, 0, len(headings))
	for _, heading := range headings {
		if heading != "" {
			path = append(path, heading)
		}
	}
	return path
}

func lineStarts(source []byte) []int {
	starts := []int{0}
	for index, value := range source {
		if value == '\n' && index+1 < len(source) {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func rawLines(source []byte, starts []int, startLine, endLine int) string {
	if startLine <= 0 || endLine < startLine || startLine > len(starts) {
		return ""
	}
	start := starts[startLine-1]
	stop := len(source)
	if endLine < len(starts) {
		stop = starts[endLine]
	}
	return strings.TrimSuffix(string(source[start:stop]), "\n")
}

func fencedCodeSourceRange(source []byte, starts []int, fromLine, contentStart, contentEnd int) (int, int) {
	if fromLine < 1 {
		fromLine = 1
	}
	searchEnd := len(starts)
	if contentStart > 0 {
		searchEnd = contentStart - 1
	}
	for line := fromLine; line <= searchEnd; line++ {
		marker, length, ok := openingFence(sourceLine(source, starts, line))
		if !ok {
			continue
		}
		for closingLine := line + 1; closingLine <= len(starts); closingLine++ {
			if isClosingFence(sourceLine(source, starts, closingLine), marker, length) {
				return line, closingLine
			}
		}
		return line, len(starts)
	}
	return contentStart, contentEnd
}

func sourceLine(source []byte, starts []int, line int) []byte {
	if line < 1 || line > len(starts) {
		return nil
	}
	start := starts[line-1]
	stop := len(source)
	if line < len(starts) {
		stop = starts[line] - 1
	}
	return source[start:stop]
}

func openingFence(line []byte) (byte, int, bool) {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	if index >= len(line) || (line[index] != '`' && line[index] != '~') {
		return 0, 0, false
	}
	marker := line[index]
	start := index
	for index < len(line) && line[index] == marker {
		index++
	}
	length := index - start
	return marker, length, length >= 3
}

func isClosingFence(line []byte, marker byte, openingLength int) bool {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	start := index
	for index < len(line) && line[index] == marker {
		index++
	}
	return index-start >= openingLength && strings.TrimSpace(string(line[index:])) == ""
}
