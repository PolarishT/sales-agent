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
	appendNormalizedList(&lines, list, source, 0)
	return strings.Join(lines, "\n")
}

func normalizeCode(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	return strings.Trim(raw, "\n")
}

func parseFragment(source []byte) ast.Node {
	markdown := goldmark.New(goldmark.WithExtensions(extension.GFM))
	return markdown.Parser().Parse(text.NewReader(source))
}

func appendNormalizedList(lines *[]string, list *ast.List, source []byte, depth int) {
	number := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		bodyParts := make([]string, 0, item.ChildCount())
		nested := make([]*ast.List, 0, 1)
		for child := item.FirstChild(); child != nil; child = child.NextSibling() {
			if childList, ok := child.(*ast.List); ok {
				nested = append(nested, childList)
				continue
			}
			if value := normalizeWhitespace(nodeText(child, source)); value != "" {
				bodyParts = append(bodyParts, value)
			}
		}
		marker := "-"
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d.", number)
			number++
		}
		*lines = append(*lines, strings.Repeat("  ", depth)+marker+" "+strings.Join(bodyParts, " "))
		for _, childList := range nested {
			appendNormalizedList(lines, childList, source, depth+1)
		}
	}
}
