package domain

type BlockType string

const (
	BlockParagraph     BlockType = "paragraph"
	BlockList          BlockType = "list"
	BlockTable         BlockType = "table"
	BlockQuote         BlockType = "quote"
	BlockCode          BlockType = "code"
	BlockRawHTML       BlockType = "raw_html"
	BlockImage         BlockType = "image"
	BlockThematicBreak BlockType = "thematic_break"
)

type MarkdownBlock struct {
	Type        BlockType
	HeadingPath []string
	RawContent  string
	Content     string
	StartLine   int
	EndLine     int
	Ordinal     int
}

type ParsedDocument struct {
	Blocks []MarkdownBlock
}

type NormalizedDocument struct {
	Blocks []MarkdownBlock
}

type ChunkConfig struct {
	ChunkSize    int
	ChunkOverlap int
}

type Chunk struct {
	ChunkIndex       int
	Content          string
	EmbeddingContent string
	HeadingPath      []string
	StartLine        int
	EndLine          int
	EstimatedTokens  int
	ContentHash      string
}

type EmbeddedChunk struct {
	Chunk
	Vector []float64
}
