package markdown

import (
	"context"
	"testing"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
)

func TestFilterAppliesOnlyDeterministicRules(t *testing.T) {
	document := domain.ParsedDocument{Blocks: []domain.MarkdownBlock{
		{Type: domain.BlockRawHTML, RawContent: "<!-- internal -->", Content: "<!-- internal -->"},
		{Type: domain.BlockThematicBreak, RawContent: "---", Content: "---"},
		{Type: domain.BlockParagraph, RawContent: "  \n", Content: " \t"},
		{Type: domain.BlockImage, RawContent: "![](photo.jpg)", Content: ""},
		{Type: domain.BlockParagraph, RawContent: "IP68", Content: "IP68", Ordinal: 4},
		{Type: domain.BlockImage, RawContent: "![正面图](photo.jpg)", Content: "正面图", Ordinal: 5},
	}}

	filtered, err := NewFilter().Apply(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Blocks) != 2 {
		t.Fatalf("blocks = %+v", filtered.Blocks)
	}
	if got := filtered.Blocks[0].Content; got != "IP68" {
		t.Fatalf("short fact = %q", got)
	}
	if got := filtered.Blocks[1]; got.Type != domain.BlockImage || got.Content != "正面图" {
		t.Fatalf("image = %+v", got)
	}
	if filtered.Blocks[0].Ordinal != 4 || filtered.Blocks[1].Ordinal != 5 {
		t.Fatalf("filter changed ordinals: %+v", filtered.Blocks)
	}
}

func TestFilterRejectsDocumentWithNoIndexableContent(t *testing.T) {
	document := domain.ParsedDocument{Blocks: []domain.MarkdownBlock{
		{Type: domain.BlockRawHTML, RawContent: "<script>x</script>"},
		{Type: domain.BlockImage, RawContent: "![](photo.jpg)"},
	}}

	_, err := NewFilter().Apply(context.Background(), document)
	if !domain.IsCode(err, domain.CodeNoIndexableContent) {
		t.Fatalf("error = %v, want %s", err, domain.CodeNoIndexableContent)
	}
}
