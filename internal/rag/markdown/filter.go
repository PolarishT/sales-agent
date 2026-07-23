package markdown

import (
	"context"
	"strings"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
)

type Filter struct{}

func NewFilter() *Filter {
	return &Filter{}
}

func (f *Filter) Apply(ctx context.Context, document domain.ParsedDocument) (domain.ParsedDocument, error) {
	filtered := make([]domain.MarkdownBlock, 0, len(document.Blocks))
	for _, block := range document.Blocks {
		if err := ctx.Err(); err != nil {
			return domain.ParsedDocument{}, err
		}
		if block.Type == domain.BlockRawHTML || block.Type == domain.BlockThematicBreak {
			continue
		}
		if strings.TrimSpace(block.Content) == "" {
			continue
		}
		if block.Type == domain.BlockImage && strings.TrimSpace(block.Content) == "" {
			continue
		}
		filtered = append(filtered, block)
	}
	if len(filtered) == 0 {
		return domain.ParsedDocument{}, domain.NewError(
			domain.CodeNoIndexableContent,
			"文档没有可索引内容",
			nil,
		)
	}
	return domain.ParsedDocument{Blocks: filtered}, nil
}
