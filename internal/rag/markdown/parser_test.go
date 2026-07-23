package markdown

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
)

func TestParserPreservesMarkdownStructure(t *testing.T) {
	source := []byte(`# 手机

短事实：IP68

## 摄像头

- 主摄 4800 万像素
- 支持 5 倍光学变焦

| 规格 | 值 |
| --- | --- |
| 重量 | 199g |

<!-- internal note -->
<script>alert("x")</script>
`)

	document, err := NewParser().Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	blocks := document.Blocks
	if len(blocks) < 2 {
		t.Fatalf("block count = %d, want at least 2", len(blocks))
	}
	if got := blocks[0].HeadingPath; !reflect.DeepEqual(got, []string{"手机"}) {
		t.Fatalf("first heading path = %#v", got)
	}
	if got := blocks[1].HeadingPath; !reflect.DeepEqual(got, []string{"手机", "摄像头"}) {
		t.Fatalf("second heading path = %#v", got)
	}
	if blocks[0].StartLine <= 0 || blocks[0].EndLine < blocks[0].StartLine {
		t.Fatalf("invalid source range = %d..%d", blocks[0].StartLine, blocks[0].EndLine)
	}
	if blocks[0].RawContent == "" || blocks[0].Content != "短事实：IP68" {
		t.Fatalf("first block = %+v", blocks[0])
	}

	assertBlockType(t, blocks, domain.BlockList)
	assertBlockType(t, blocks, domain.BlockTable)
	rawHTMLCount := 0
	for _, block := range blocks {
		if block.Type == domain.BlockRawHTML {
			rawHTMLCount++
			if strings.TrimSpace(block.RawContent) == "" {
				t.Fatalf("raw HTML block has no source: %+v", block)
			}
		}
	}
	if rawHTMLCount != 2 {
		t.Fatalf("raw HTML block count = %d, want 2; blocks = %+v", rawHTMLCount, blocks)
	}
	for index, block := range blocks {
		if block.Ordinal != index {
			t.Fatalf("block %d ordinal = %d", index, block.Ordinal)
		}
	}
}

func TestParserPreservesStandaloneImagesAndThematicBreaks(t *testing.T) {
	document, err := NewParser().Parse(context.Background(), []byte("---\n\n![正面图](front.jpg)\n\n![](empty.jpg)\n"))
	if err != nil {
		t.Fatal(err)
	}

	if len(document.Blocks) != 3 {
		t.Fatalf("blocks = %+v", document.Blocks)
	}
	if got := document.Blocks[0].Type; got != domain.BlockThematicBreak {
		t.Fatalf("thematic break type = %q", got)
	}
	if got := document.Blocks[0]; got.StartLine != 1 || got.EndLine != 1 || got.RawContent != "---" {
		t.Fatalf("thematic break source = %+v", got)
	}
	if got := document.Blocks[1]; got.Type != domain.BlockImage || got.Content != "正面图" {
		t.Fatalf("image block = %+v", got)
	}
	if got := document.Blocks[2]; got.Type != domain.BlockImage || got.Content != "" {
		t.Fatalf("empty-alt image block = %+v", got)
	}
}

func TestParserPreservesCodeInsideContainers(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		blockType  domain.BlockType
		wantPrefix string
		wantCode   string
	}{
		{
			name:      "list with fenced code only",
			source:    "- ```text\n    IP68\n  ```\n",
			blockType: domain.BlockList,
			wantCode:  "  IP68",
		},
		{
			name:       "list with text and indented code",
			source:     "- 规格\n\n      IP68\n      199g\n",
			blockType:  domain.BlockList,
			wantPrefix: "规格\n",
			wantCode:   "IP68\n199g",
		},
		{
			name:      "quote with indented code only",
			source:    ">     IP68\n>       199g\n",
			blockType: domain.BlockQuote,
			wantCode:  "IP68\n  199g",
		},
		{
			name:       "quote with text and fenced code",
			source:     "> 规格\n>\n> ```text\n>   IP68\n> ```\n",
			blockType:  domain.BlockQuote,
			wantPrefix: "规格\n",
			wantCode:   "  IP68",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := NewParser().Parse(context.Background(), []byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Blocks) != 1 || parsed.Blocks[0].Type != test.blockType {
				t.Fatalf("blocks = %+v", parsed.Blocks)
			}
			content := parsed.Blocks[0].Content
			if test.wantPrefix != "" && !strings.HasPrefix(content, test.wantPrefix) {
				t.Fatalf("content = %q, want prefix %q", content, test.wantPrefix)
			}
			if !strings.Contains(content, test.wantCode) {
				t.Fatalf("content = %q, want code %q", content, test.wantCode)
			}
			if _, err := NewFilter().Apply(context.Background(), parsed); err != nil {
				t.Fatalf("filter removed code container: %v", err)
			}
		})
	}
}

func TestParserIncludesFencesInCodeSourceRange(t *testing.T) {
	source := []byte("# 商品\n\n```go\nline one\n  line two\n```\n")

	document, err := NewParser().Parse(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 1 {
		t.Fatalf("blocks = %+v", document.Blocks)
	}
	block := document.Blocks[0]
	if block.StartLine != 3 || block.EndLine != 6 {
		t.Fatalf("code source range = %d..%d, want 3..6", block.StartLine, block.EndLine)
	}
	if block.RawContent != "```go\nline one\n  line two\n```" {
		t.Fatalf("raw code = %q", block.RawContent)
	}
	if block.Content != "line one\n  line two\n" {
		t.Fatalf("visible code = %q", block.Content)
	}
}

func assertBlockType(t *testing.T, blocks []domain.MarkdownBlock, want domain.BlockType) {
	t.Helper()
	for _, block := range blocks {
		if block.Type == want {
			return
		}
	}
	t.Fatalf("no block of type %q in %+v", want, blocks)
}
