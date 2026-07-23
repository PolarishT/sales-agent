package markdown

import (
	"context"
	"fmt"
	"strings"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

type Normalizer struct{}

func NewNormalizer() *Normalizer {
	return &Normalizer{}
}

func (n *Normalizer) Normalize(ctx context.Context, document domain.ParsedDocument) (domain.NormalizedDocument, error) {
	blocks := make([]domain.MarkdownBlock, 0, len(document.Blocks))
	for _, original := range document.Blocks {
		if err := ctx.Err(); err != nil {
			return domain.NormalizedDocument{}, err
		}
		block := original
		block.HeadingPath = append([]string(nil), original.HeadingPath...)
		switch block.Type {
		case domain.BlockTable:
			block.Content = normalizeTable(block.RawContent)
		case domain.BlockList:
			block.Content = normalizeList(block.RawContent)
		case domain.BlockQuote:
			block.Content = normalizeQuote(block.RawContent)
		case domain.BlockCode:
			block.Content = normalizeCode(block.Content)
		default:
			block.Content = normalizeWhitespace(block.Content)
		}
		for index, heading := range block.HeadingPath {
			block.HeadingPath[index] = normalizeWhitespace(heading)
		}
		blocks = append(blocks, block)
	}
	return domain.NormalizedDocument{Blocks: blocks}, nil
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeTable(raw string) string {
	source := []byte(raw)
	root := parseFragment(source)
	var table *extensionast.Table
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && table == nil {
			table, _ = node.(*extensionast.Table)
		}
		return ast.WalkContinue, nil
	})
	if table == nil {
		return normalizeWhitespace(raw)
	}

	lines := make([]string, 0, table.ChildCount()+1)
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		cells := make([]string, 0, row.ChildCount())
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			value := normalizeWhitespace(nodeText(cell, source))
			value = strings.ReplaceAll(value, "|", `\|`)
			cells = append(cells, value)
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
		if _, ok := row.(*extensionast.TableHeader); ok {
			alignments := make([]string, len(table.Alignments))
			for index, alignment := range table.Alignments {
				switch alignment {
				case extensionast.AlignLeft:
					alignments[index] = ":---"
				case extensionast.AlignRight:
					alignments[index] = "---:"
				case extensionast.AlignCenter:
					alignments[index] = ":---:"
				default:
					alignments[index] = "---"
				}
			}
			lines = append(lines, "| "+strings.Join(alignments, " | ")+" |")
		}
	}
	return strings.Join(lines, "\n")
}

func normalizeList(raw string) string {
	source := []byte(raw)
	root := parseFragment(source)
	var list *ast.List
	for child := root.FirstChild(); child != nil; child = child.NextSibling() {
		if candidate, ok := child.(*ast.List); ok {
			list = candidate
			break
		}
	}
	if list == nil {
		return normalizeWhitespace(raw)
	}
	lines := make([]string, 0, list.ChildCount())
	appendNormalizedList(&lines, list, source, "")
	return strings.Join(lines, "\n")
}

func normalizeQuote(raw string) string {
	source := []byte(raw)
	root := parseFragment(source)
	var quote *ast.Blockquote
	for child := root.FirstChild(); child != nil; child = child.NextSibling() {
		if candidate, ok := child.(*ast.Blockquote); ok {
			quote = candidate
			break
		}
	}
	if quote == nil {
		return normalizeWhitespace(raw)
	}

	parts := make([]string, 0, quote.ChildCount())
	for child := quote.FirstChild(); child != nil; child = child.NextSibling() {
		var value string
		switch typed := child.(type) {
		case *ast.CodeBlock:
			value = normalizeCode(string(typed.Lines().Value(source)))
		case *ast.FencedCodeBlock:
			value = normalizeCode(string(typed.Lines().Value(source)))
		case *ast.HTMLBlock, *ast.ThematicBreak:
			continue
		default:
			value = normalizeWhitespace(nodeText(child, source))
		}
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "\n")
}

func normalizeCode(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	return strings.TrimSuffix(raw, "\n")
}

func parseFragment(source []byte) ast.Node {
	markdown := goldmark.New(goldmark.WithExtensions(extension.GFM))
	return markdown.Parser().Parse(text.NewReader(source))
}

func appendNormalizedList(lines *[]string, list *ast.List, source []byte, indent string) {
	number := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		marker := "-"
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d.", number)
			number++
		}
		itemPrefix := indent + marker
		contentIndent := indent + strings.Repeat(" ", len(marker)+1)
		wroteMarker := false

		for child := item.FirstChild(); child != nil; child = child.NextSibling() {
			if childList, ok := child.(*ast.List); ok {
				if !wroteMarker {
					*lines = append(*lines, itemPrefix)
					wroteMarker = true
				}
				appendNormalizedList(lines, childList, source, contentIndent)
				continue
			}

			if code, language, ok := listItemCode(child, source); ok {
				opening := "```" + language
				if !wroteMarker {
					*lines = append(*lines, itemPrefix+" "+opening)
					wroteMarker = true
				} else {
					*lines = append(*lines, contentIndent+opening)
				}
				if code != "" {
					for _, line := range strings.Split(code, "\n") {
						*lines = append(*lines, contentIndent+line)
					}
				}
				*lines = append(*lines, contentIndent+"```")
				continue
			}

			value := normalizeWhitespace(nodeText(child, source))
			if value == "" {
				continue
			}
			if !wroteMarker {
				*lines = append(*lines, itemPrefix+" "+value)
				wroteMarker = true
			} else {
				*lines = append(*lines, contentIndent+value)
			}
		}
		if !wroteMarker {
			*lines = append(*lines, itemPrefix)
		}
	}
}

func listItemCode(node ast.Node, source []byte) (string, string, bool) {
	switch typed := node.(type) {
	case *ast.CodeBlock:
		return normalizeCode(string(typed.Lines().Value(source))), "", true
	case *ast.FencedCodeBlock:
		return normalizeCode(string(typed.Lines().Value(source))), string(typed.Language(source)), true
	default:
		return "", "", false
	}
}
