package splitter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
)

func TestConservativeEstimatorUsesDocumentedWeights(t *testing.T) {
	estimator := ConservativeEstimator{}

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{name: "whitespace", input: " \t\n", want: 0},
		{name: "ascii", input: "abc123!?", want: 3},
		{name: "ascii without floating drift", input: strings.Repeat("a", 100), want: 30},
		{name: "unclassified ascii symbol", input: "$", want: 2},
		{name: "han ascii emoji", input: "汉A!🙂", want: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := estimator.Estimate(test.input); got != test.want {
				t.Fatalf("Estimate(%q) = %d, want %d", test.input, got, test.want)
			}
		})
	}
}

func TestSplitterPacksBlocksAndEndsChunkOnHeadingChange(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{
		{
			Type:        domain.BlockParagraph,
			HeadingPath: []string{"商品"},
			Content:     "abcdefghij",
			StartLine:   3,
			EndLine:     3,
		},
		{
			Type:        domain.BlockParagraph,
			HeadingPath: []string{"商品"},
			Content:     "klmnopqrst",
			StartLine:   5,
			EndLine:     5,
		},
		{
			Type:        domain.BlockParagraph,
			HeadingPath: []string{"商品", "颜色"},
			Content:     "颜色：黑色",
			StartLine:   8,
			EndLine:     8,
		},
	}}

	config := domain.ChunkConfig{ChunkSize: 8, ChunkOverlap: 2}
	chunks, err := New().Split(context.Background(), document, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if got := chunks[0].Content; got != "abcdefghij\n\nklmnopqrst" {
		t.Fatalf("packed content = %q", got)
	}
	if got := chunks[1].Content; got != "颜色：黑色" {
		t.Fatalf("short fact = %q", got)
	}
	if chunks[0].StartLine != 3 || chunks[0].EndLine != 5 {
		t.Fatalf("first source range = %d..%d", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 8 || chunks[1].EndLine != 8 {
		t.Fatalf("second source range = %d..%d", chunks[1].StartLine, chunks[1].EndLine)
	}
	if !reflect.DeepEqual(chunks[1].HeadingPath, []string{"商品", "颜色"}) {
		t.Fatalf("heading path = %#v", chunks[1].HeadingPath)
	}
	for index, chunk := range chunks {
		if chunk.ChunkIndex != index {
			t.Fatalf("chunk %d index = %d", index, chunk.ChunkIndex)
		}
		wantPrefix := strings.Join(chunk.HeadingPath, " > ")
		if !strings.HasPrefix(chunk.EmbeddingContent, wantPrefix) {
			t.Fatalf("embedding content = %q, want prefix %q", chunk.EmbeddingContent, wantPrefix)
		}
		sum := sha256.Sum256([]byte(chunk.EmbeddingContent))
		if chunk.ContentHash != hex.EncodeToString(sum[:]) {
			t.Fatalf("content hash = %q", chunk.ContentHash)
		}
		if chunk.EstimatedTokens != (ConservativeEstimator{}).Estimate(chunk.EmbeddingContent) {
			t.Fatalf("estimated tokens = %d for %q", chunk.EstimatedTokens, chunk.EmbeddingContent)
		}
	}
}

func TestSplitterPrependsCompleteSemanticOverlap(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{
		{Type: domain.BlockParagraph, HeadingPath: []string{"商品"}, Content: "abcdefghijklmnopqrst", StartLine: 1, EndLine: 1},
		{Type: domain.BlockParagraph, HeadingPath: []string{"商品"}, Content: "small", StartLine: 2, EndLine: 2},
		{Type: domain.BlockParagraph, HeadingPath: []string{"商品"}, Content: "uvwxyzabcdefghijklmn", StartLine: 3, EndLine: 3},
	}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    8,
		ChunkOverlap: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if !strings.HasPrefix(chunks[1].Content, "small\n\n") {
		t.Fatalf("second chunk has no semantic overlap: %q", chunks[1].Content)
	}
	if chunks[1].StartLine != 2 || chunks[1].EndLine != 3 {
		t.Fatalf("overlapped source range = %d..%d", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestSplitterBreaksOversizedTextAtSentenceAndKeepsUnicodeValid(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockParagraph,
		HeadingPath: []string{"描述"},
		Content:     "甲乙丙。丁戊己。🙂🙂🙂🙂🙂🙂",
		StartLine:   4,
		EndLine:     4,
	}}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    8,
		ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Content != "甲乙丙。" || chunks[1].Content != "丁戊己。" {
		t.Fatalf("sentence boundaries = %q / %q", chunks[0].Content, chunks[1].Content)
	}
	for _, chunk := range chunks {
		if !utf8.ValidString(chunk.Content) || !utf8.ValidString(chunk.EmbeddingContent) {
			t.Fatalf("invalid UTF-8 chunk: %+v", chunk)
		}
	}
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		rebuilt.WriteString(chunk.Content)
	}
	if rebuilt.String() != document.Blocks[0].Content {
		t.Fatalf("rune fallback changed content: %q", rebuilt.String())
	}
}

func TestSplitterUsesMeaningfulRuneSafeSuffixForSplitTextOverlap(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockParagraph,
		HeadingPath: []string{"描述"},
		Content:     "甲乙丙。丁戊己。",
		StartLine:   4,
		EndLine:     4,
	}}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    8,
		ChunkOverlap: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if !strings.HasPrefix(chunks[1].Content, "丙。\n\n") {
		t.Fatalf("rune-safe suffix overlap = %q", chunks[1].Content)
	}
}

func TestSplitterBreaksOversizedEnglishTextAtSentenceEnd(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:      domain.BlockParagraph,
		Content:   "first sentence. second sentence.",
		StartLine: 1,
		EndLine:   1,
	}}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    5,
		ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].Content != "first sentence." || chunks[1].Content != "second sentence." {
		t.Fatalf("english sentence boundaries = %+v", chunks)
	}
}

func TestSplitterRepeatsHeaderForOversizedTable(t *testing.T) {
	const header = "| 规格 | 值 |\n| --- | --- |"
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockTable,
		HeadingPath: []string{"规格"},
		Content:     header + "\n| 颜色 | 黑色 |\n| 重量 | 199g |",
		StartLine:   10,
		EndLine:     13,
	}}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    12,
		ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	for _, chunk := range chunks {
		if !strings.HasPrefix(chunk.Content, header+"\n") {
			t.Fatalf("table header was not repeated: %q", chunk.Content)
		}
	}
	if strings.Contains(chunks[0].Content, "重量") || strings.Contains(chunks[1].Content, "颜色") {
		t.Fatalf("table rows were not split in order: %+v", chunks)
	}
}

func TestSplitterKeepsOversizedListItemsWhole(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockList,
		HeadingPath: []string{"卖点"},
		Content:     "- 第一项完整内容\n- 第二项完整内容\n  延续说明",
		StartLine:   2,
		EndLine:     4,
	}}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    12,
		ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Content != "- 第一项完整内容" {
		t.Fatalf("first list item = %q", chunks[0].Content)
	}
	if chunks[1].Content != "- 第二项完整内容\n  延续说明" {
		t.Fatalf("second list item was split: %q", chunks[1].Content)
	}
}

func TestSplitterKeepsOversizedCodeLinesWhole(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockCode,
		HeadingPath: []string{"示例"},
		Content:     "first-line-1234567890\nsecond-line-abcdefghij",
		StartLine:   7,
		EndLine:     10,
	}}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    8,
		ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Content != "first-line-1234567890" || chunks[1].Content != "second-line-abcdefghij" {
		t.Fatalf("code was not split on line boundaries: %+v", chunks)
	}
}

func TestSplitterKeepsCodeBoundaryBlankLinesWithOversizedLine(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockCode,
		HeadingPath: []string{"示例"},
		Content:     "\nfirst-line-12345678901234567890\nsecond\n",
		StartLine:   7,
		EndLine:     11,
	}}}

	chunks, err := New().Split(context.Background(), document, domain.ChunkConfig{
		ChunkSize:    8,
		ChunkOverlap: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Content != "\nfirst-line-12345678901234567890" {
		t.Fatalf("leading blank line was lost: %q", chunks[0].Content)
	}
	if chunks[1].Content != "second\n" {
		t.Fatalf("trailing blank line was lost: %q", chunks[1].Content)
	}
}

func TestSplitterIsDeterministic(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{
		{Type: domain.BlockParagraph, HeadingPath: []string{"商品"}, Content: "甲乙丙。丁戊己。", StartLine: 1, EndLine: 1},
		{Type: domain.BlockParagraph, HeadingPath: []string{"商品"}, Content: "颜色：黑色", StartLine: 2, EndLine: 2},
	}}
	config := domain.ChunkConfig{ChunkSize: 8, ChunkOverlap: 2}

	first, err := New().Split(context.Background(), document, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New().Split(context.Background(), document, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("split is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	for _, chunk := range first {
		if len(chunk.ContentHash) != sha256.Size*2 {
			t.Fatalf("hash = %q", chunk.ContentHash)
		}
	}
}

func TestSplitterRejectsInvalidConfigAndCanceledContext(t *testing.T) {
	splitter := New()
	for _, config := range []domain.ChunkConfig{
		{},
		{ChunkSize: 8, ChunkOverlap: -1},
		{ChunkSize: 8, ChunkOverlap: 8},
		{ChunkSize: 8, ChunkOverlap: 9},
	} {
		_, err := splitter.Split(context.Background(), domain.NormalizedDocument{}, config)
		if !domain.IsCode(err, domain.CodeDocumentSplitFailed) {
			t.Fatalf("config %+v error = %v", config, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := splitter.Split(ctx, domain.NormalizedDocument{}, domain.ChunkConfig{ChunkSize: 8})
	if err != context.Canceled {
		t.Fatalf("canceled error = %v", err)
	}
}
