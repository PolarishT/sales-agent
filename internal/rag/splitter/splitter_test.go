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

type scanningEstimator struct {
	base         ConservativeEstimator
	calls        int
	scannedRunes int
}

func (e *scanningEstimator) Estimate(content string) int {
	e.calls++
	e.scannedRunes += utf8.RuneCountInString(content)
	return e.base.Estimate(content)
}

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

func TestSplitterReusesWholeSemanticUnitWhenItSlightlyExceedsOverlap(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{
		{Type: domain.BlockParagraph, Content: strings.Repeat("a", 16), StartLine: 1, EndLine: 1},
		{Type: domain.BlockParagraph, Content: "TAILuvwxyz", StartLine: 2, EndLine: 2},
		{Type: domain.BlockParagraph, Content: strings.Repeat("n", 16), StartLine: 3, EndLine: 3},
	}}
	config := domain.ChunkConfig{ChunkSize: 8, ChunkOverlap: 2}

	chunks, err := New().Split(context.Background(), document, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if !strings.HasPrefix(chunks[1].Content, "TAILuvwxyz\n\n") {
		t.Fatalf("whole semantic overlap = %q", chunks[1].Content)
	}
	for _, chunk := range chunks {
		if got := (ConservativeEstimator{}).Estimate(chunk.Content); got > config.ChunkSize {
			t.Fatalf("chunk content estimate = %d, want <= %d: %q", got, config.ChunkSize, chunk.Content)
		}
	}
}

func TestSplitterDoesNotTruncateCompleteParagraphForOverlap(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{
		{Type: domain.BlockParagraph, Content: strings.Repeat("a", 16), StartLine: 1, EndLine: 1},
		{Type: domain.BlockParagraph, Content: "TAILuvwxyz", StartLine: 2, EndLine: 2},
		{Type: domain.BlockParagraph, Content: strings.Repeat("n", 20), StartLine: 3, EndLine: 3},
	}}
	config := domain.ChunkConfig{ChunkSize: 8, ChunkOverlap: 2}

	chunks, err := New().Split(context.Background(), document, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[1].Content != strings.Repeat("n", 20) {
		t.Fatalf("complete paragraph was truncated for overlap: %q", chunks[1].Content)
	}
}

func TestSplitterReservesBudgetBeforeAddingFreshUnit(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{
		{Type: domain.BlockParagraph, Content: strings.Repeat("a", 20), StartLine: 1, EndLine: 1},
		{Type: domain.BlockParagraph, Content: "small", StartLine: 2, EndLine: 2},
		{Type: domain.BlockParagraph, Content: strings.Repeat("n", 26), StartLine: 3, EndLine: 3},
	}}
	config := domain.ChunkConfig{ChunkSize: 8, ChunkOverlap: 2}

	chunks, err := New().Split(context.Background(), document, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if !strings.Contains(chunks[1].Content, strings.Repeat("n", 26)) {
		t.Fatalf("second chunk did not consume a fresh unit: %q", chunks[1].Content)
	}
	for _, chunk := range chunks {
		if got := (ConservativeEstimator{}).Estimate(chunk.Content); got > config.ChunkSize {
			t.Fatalf("chunk content estimate = %d, want <= %d: %q", got, config.ChunkSize, chunk.Content)
		}
	}
}

func TestSplitterDoesNotExpandPurePunctuationSuffixBeyondBudget(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:      domain.BlockParagraph,
		Content:   strings.Repeat("。", 10),
		StartLine: 1,
		EndLine:   1,
	}}}
	config := domain.ChunkConfig{ChunkSize: 8, ChunkOverlap: 2}

	chunks, err := New().Split(context.Background(), document, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	for _, chunk := range chunks {
		if got := (ConservativeEstimator{}).Estimate(chunk.Content); got > config.ChunkSize {
			t.Fatalf("chunk content estimate = %d, want <= %d: %q", got, config.ChunkSize, chunk.Content)
		}
	}
	if strings.Contains(chunks[1].Content, "\n\n") {
		t.Fatalf("pure punctuation should not be duplicated as overlap: %q", chunks[1].Content)
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

func TestSplitterSkipsSuffixWhenNoMeaningfulTextFitsOverlapBudget(t *testing.T) {
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
	if chunks[1].Content != "丁戊己。" {
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

func TestSplitterScansLongUnbrokenTextNearLinearly(t *testing.T) {
	const sourceRunes = 64 << 10
	estimator := &scanningEstimator{}
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:      domain.BlockParagraph,
		Content:   strings.Repeat("a", sourceRunes),
		StartLine: 1,
		EndLine:   1,
	}}}

	chunks, err := NewWithEstimator(estimator).Split(
		context.Background(),
		document,
		domain.ChunkConfig{ChunkSize: 64, ChunkOverlap: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunk count = %d", len(chunks))
	}
	const maximumScanFactor = 40
	if estimator.scannedRunes > sourceRunes*maximumScanFactor {
		t.Fatalf(
			"scanned runes = %d across %d calls, want <= %d",
			estimator.scannedRunes,
			estimator.calls,
			sourceRunes*maximumScanFactor,
		)
	}
}

func BenchmarkSplitterFiveMiBUnbrokenParagraph(b *testing.B) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:      domain.BlockParagraph,
		Content:   strings.Repeat("a", 5<<20),
		StartLine: 1,
		EndLine:   1,
	}}}
	config := domain.ChunkConfig{ChunkSize: 512, ChunkOverlap: 64}
	splitter := New()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := splitter.Split(context.Background(), document, config); err != nil {
			b.Fatal(err)
		}
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
	if chunks[0].StartLine != 10 || chunks[0].EndLine != 12 {
		t.Fatalf("first table source range = %d..%d", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 10 || chunks[1].EndLine != 13 {
		t.Fatalf("second table source range = %d..%d", chunks[1].StartLine, chunks[1].EndLine)
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
	if chunks[0].StartLine != 2 || chunks[0].EndLine != 2 {
		t.Fatalf("first list source range = %d..%d", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 3 || chunks[1].EndLine != 4 {
		t.Fatalf("second list source range = %d..%d", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestSplitterMapsCommonMarkListMarkersToRawSourceLines(t *testing.T) {
	tests := []struct {
		name      string
		block     domain.MarkdownBlock
		wantFirst [2]int
		wantLast  [2]int
	}{
		{
			name: "ordered parenthesis with blank continuation",
			block: domain.MarkdownBlock{
				Type:       domain.BlockList,
				RawContent: "1) 第一项完整内容\n\n   续行\n2) 第二项完整内容",
				Content:    "1. 第一项完整内容 续行\n2. 第二项完整内容",
				StartLine:  10,
				EndLine:    13,
			},
			wantFirst: [2]int{10, 12},
			wantLast:  [2]int{13, 13},
		},
		{
			name: "tab after bullet marker",
			block: domain.MarkdownBlock{
				Type:       domain.BlockList,
				RawContent: "-\t第一项完整内容\n\t续行\n\n-\t第二项完整内容",
				Content:    "- 第一项完整内容 续行\n- 第二项完整内容",
				StartLine:  20,
				EndLine:    23,
			},
			wantFirst: [2]int{20, 21},
			wantLast:  [2]int{23, 23},
		},
		{
			name: "nested marker is not top level",
			block: domain.MarkdownBlock{
				Type:       domain.BlockList,
				RawContent: "1) 父项完整内容\n   - 子项完整内容\n\n2) 第二项完整内容",
				Content:    "1. 父项完整内容\n   - 子项完整内容\n2. 第二项完整内容",
				StartLine:  30,
				EndLine:    33,
			},
			wantFirst: [2]int{30, 31},
			wantLast:  [2]int{33, 33},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks, err := New().Split(
				context.Background(),
				domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{test.block}},
				domain.ChunkConfig{ChunkSize: 12, ChunkOverlap: 0},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) != 2 {
				t.Fatalf("chunks = %+v", chunks)
			}
			if got := [2]int{chunks[0].StartLine, chunks[0].EndLine}; got != test.wantFirst {
				t.Fatalf("first source range = %v, want %v", got, test.wantFirst)
			}
			if got := [2]int{chunks[1].StartLine, chunks[1].EndLine}; got != test.wantLast {
				t.Fatalf("last source range = %v, want %v", got, test.wantLast)
			}
		})
	}
}

func TestSplitterFallsBackToWholeListRangeWhenRawMappingIsAmbiguous(t *testing.T) {
	block := domain.MarkdownBlock{
		Type:       domain.BlockList,
		RawContent: "- 第一项完整内容\n无法映射的额外原文\n- 第二项完整内容\n- 第三项额外原文",
		Content:    "- 第一项完整内容\n- 第二项完整内容",
		StartLine:  40,
		EndLine:    43,
	}

	chunks, err := New().Split(
		context.Background(),
		domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{block}},
		domain.ChunkConfig{ChunkSize: 12, ChunkOverlap: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	for _, chunk := range chunks {
		if chunk.StartLine != block.StartLine || chunk.EndLine != block.EndLine {
			t.Fatalf("ambiguous source range = %d..%d, want %d..%d", chunk.StartLine, chunk.EndLine, block.StartLine, block.EndLine)
		}
	}
}

func TestSplitterKeepsOversizedCodeLinesWhole(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockCode,
		HeadingPath: []string{"示例"},
		RawContent:  "```text\nfirst-line-1234567890\nsecond-line-abcdefghij\n```",
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
	if chunks[0].StartLine != 8 || chunks[0].EndLine != 8 {
		t.Fatalf("first code source range = %d..%d", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 9 || chunks[1].EndLine != 9 {
		t.Fatalf("second code source range = %d..%d", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestSplitterMapsUnclosedFencedCodeContentLines(t *testing.T) {
	document := domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{
		Type:        domain.BlockCode,
		HeadingPath: []string{"示例"},
		RawContent:  "```text\nfirst-line-1234567890\nsecond-line-abcdefghij",
		Content:     "first-line-1234567890\nsecond-line-abcdefghij",
		StartLine:   7,
		EndLine:     9,
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
	if chunks[0].StartLine != 8 || chunks[0].EndLine != 8 {
		t.Fatalf("first unclosed code source range = %d..%d", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 9 || chunks[1].EndLine != 9 {
		t.Fatalf("second unclosed code source range = %d..%d", chunks[1].StartLine, chunks[1].EndLine)
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
