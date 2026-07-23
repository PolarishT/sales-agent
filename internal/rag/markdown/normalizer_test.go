package markdown

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
)

func TestNormalizerCanonicalizesVisibleMarkdownDeterministically(t *testing.T) {
	source := []byte(`---

手机
====

查看[完整规格](https://example.com/spec)。

![正面图](front.jpg "商品正面")

* 主摄 4800 万像素
* 支持 5 倍光学变焦

| 规格|值 |
|:---|---:|
| 重量 | 199g|

` + "```go\n" + "line one\n  line two\n" + "```\n")

	parsed, err := NewParser().Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := NewFilter().Apply(context.Background(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewNormalizer().Normalize(context.Background(), filtered)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewNormalizer().Normalize(context.Background(), filtered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("normalization is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}

	paragraph := blockByType(t, first.Blocks, domain.BlockParagraph)
	if paragraph.Content != "查看完整规格。" || strings.Contains(paragraph.Content, "https://") {
		t.Fatalf("paragraph = %q", paragraph.Content)
	}
	if !reflect.DeepEqual(paragraph.HeadingPath, []string{"手机"}) {
		t.Fatalf("Setext heading path = %#v", paragraph.HeadingPath)
	}

	image := blockByType(t, first.Blocks, domain.BlockImage)
	if image.Content != "正面图" {
		t.Fatalf("image = %q", image.Content)
	}

	list := blockByType(t, first.Blocks, domain.BlockList)
	if list.Content != "- 主摄 4800 万像素\n- 支持 5 倍光学变焦" {
		t.Fatalf("list = %q", list.Content)
	}

	table := blockByType(t, first.Blocks, domain.BlockTable)
	if table.Content != "| 规格 | 值 |\n| :--- | ---: |\n| 重量 | 199g |" {
		t.Fatalf("table = %q", table.Content)
	}

	code := blockByType(t, first.Blocks, domain.BlockCode)
	if code.Content != "line one\n  line two" {
		t.Fatalf("code whitespace changed: %q", code.Content)
	}
}

func TestParserTreatsDashesAsMarkdownNotFrontMatter(t *testing.T) {
	source := []byte("---\n\n产品\n====\n\n颜色： 黑色\n")

	parsed, err := NewParser().Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Blocks) != 2 || parsed.Blocks[0].Type != domain.BlockThematicBreak {
		t.Fatalf("parsed blocks = %+v", parsed.Blocks)
	}
	filtered, err := NewFilter().Apply(context.Background(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NewNormalizer().Normalize(context.Background(), filtered)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Blocks) != 1 {
		t.Fatalf("normalized blocks = %+v", normalized.Blocks)
	}
	if got := normalized.Blocks[0]; got.Content != "颜色： 黑色" || !reflect.DeepEqual(got.HeadingPath, []string{"产品"}) {
		t.Fatalf("normalized block = %+v", got)
	}
}

func TestNormalizerDoesNotMutateParsedDocument(t *testing.T) {
	document := domain.ParsedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockParagraph,
		HeadingPath: []string{"  手机  规格  "},
		Content:     "IP68",
	}}}

	_, err := NewNormalizer().Normalize(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if got := document.Blocks[0].HeadingPath[0]; got != "  手机  规格  " {
		t.Fatalf("input heading was mutated to %q", got)
	}
}

func blockByType(t *testing.T, blocks []domain.MarkdownBlock, want domain.BlockType) domain.MarkdownBlock {
	t.Helper()
	for _, block := range blocks {
		if block.Type == want {
			return block
		}
	}
	t.Fatalf("no block of type %q in %+v", want, blocks)
	return domain.MarkdownBlock{}
}
