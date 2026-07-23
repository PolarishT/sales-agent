package ingestion

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

func TestPipelineRunsStagesInOrderAndAttachesVectorsByIndex(t *testing.T) {
	repository := &fakeRepository{
		source: domain.Upload{Markdown: []byte("# 商品")},
	}
	chunks := []domain.Chunk{
		{ChunkIndex: 0, EmbeddingContent: "first"},
		{ChunkIndex: 1, EmbeddingContent: "second"},
	}
	vectors := [][]float64{vectorWithFirstValue(1), vectorWithFirstValue(2)}
	pipeline := mustPipeline(t, repository, &fakeParser{
		repository: repository,
		document:   domain.ParsedDocument{Blocks: []domain.MarkdownBlock{{Content: "parsed"}}},
	}, &fakeFilter{
		repository: repository,
		document:   domain.ParsedDocument{Blocks: []domain.MarkdownBlock{{Content: "filtered"}}},
	}, &fakeNormalizer{
		repository: repository,
		document:   domain.NormalizedDocument{Blocks: []domain.MarkdownBlock{{Content: "normalized"}}},
	}, &fakeSplitter{
		repository: repository,
		chunks:     chunks,
	}, &fakeEmbedder{
		repository: repository,
		vectors:    vectors,
	})

	if err := pipeline.Run(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}

	events, _ := repository.snapshot()
	want := []string{
		"load",
		"stage:PARSING",
		"parse",
		"stage:FILTERING",
		"filter",
		"stage:NORMALIZING",
		"normalize",
		"stage:CHUNKING",
		"split",
		"progress:chunks",
		"stage:EMBEDDING",
		"embed",
		"progress:embedded",
		"stage:STORING",
		"activate",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if len(repository.activated) != 2 ||
		repository.activated[0].Vector[0] != 1 ||
		repository.activated[1].Vector[0] != 2 {
		t.Fatalf("activated vectors = %#v", repository.activated)
	}
}

func TestPipelineMapsComponentFailuresAndStops(t *testing.T) {
	componentFailure := errors.New("supplier detail")
	tests := []struct {
		name string
		code string
		last string
		edit func(*fakeRepository, *fakeParser, *fakeFilter, *fakeNormalizer, *fakeSplitter, *fakeEmbedder)
	}{
		{
			name: "parser",
			code: domain.CodeMarkdownParseFailed,
			last: "parse",
			edit: func(_ *fakeRepository, parser *fakeParser, _ *fakeFilter, _ *fakeNormalizer, _ *fakeSplitter, _ *fakeEmbedder) {
				parser.err = componentFailure
			},
		},
		{
			name: "filter",
			code: domain.CodeNoIndexableContent,
			last: "filter",
			edit: func(_ *fakeRepository, _ *fakeParser, filter *fakeFilter, _ *fakeNormalizer, _ *fakeSplitter, _ *fakeEmbedder) {
				filter.err = componentFailure
			},
		},
		{
			name: "normalizer",
			code: domain.CodeNoIndexableContent,
			last: "normalize",
			edit: func(_ *fakeRepository, _ *fakeParser, _ *fakeFilter, normalizer *fakeNormalizer, _ *fakeSplitter, _ *fakeEmbedder) {
				normalizer.err = componentFailure
			},
		},
		{
			name: "splitter",
			code: domain.CodeDocumentSplitFailed,
			last: "split",
			edit: func(_ *fakeRepository, _ *fakeParser, _ *fakeFilter, _ *fakeNormalizer, splitter *fakeSplitter, _ *fakeEmbedder) {
				splitter.err = componentFailure
			},
		},
		{
			name: "embedder",
			code: domain.CodeEmbeddingFailed,
			last: "embed",
			edit: func(_ *fakeRepository, _ *fakeParser, _ *fakeFilter, _ *fakeNormalizer, _ *fakeSplitter, embedder *fakeEmbedder) {
				embedder.err = componentFailure
			},
		},
		{
			name: "repository stage",
			code: domain.CodeDocumentStoreFailed,
			last: "stage:CHUNKING",
			edit: func(repository *fakeRepository, _ *fakeParser, _ *fakeFilter, _ *fakeNormalizer, _ *fakeSplitter, _ *fakeEmbedder) {
				repository.stageErr = map[domain.Stage]error{domain.StageChunking: componentFailure}
			},
		},
		{
			name: "repository activate",
			code: domain.CodeDocumentStoreFailed,
			last: "activate",
			edit: func(repository *fakeRepository, _ *fakeParser, _ *fakeFilter, _ *fakeNormalizer, _ *fakeSplitter, _ *fakeEmbedder) {
				repository.activateErr = componentFailure
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{
				source:   domain.Upload{Markdown: []byte("# 商品")},
				stageErr: make(map[domain.Stage]error),
			}
			parser := &fakeParser{repository: repository, document: domain.ParsedDocument{
				Blocks: []domain.MarkdownBlock{{Content: "parsed"}},
			}}
			filter := &fakeFilter{repository: repository, document: parser.document}
			normalizer := &fakeNormalizer{repository: repository, document: domain.NormalizedDocument{
				Blocks: parser.document.Blocks,
			}}
			splitter := &fakeSplitter{repository: repository, chunks: []domain.Chunk{{
				EmbeddingContent: "content",
			}}}
			embedder := &fakeEmbedder{repository: repository, vectors: [][]float64{
				vectorWithFirstValue(1),
			}}
			test.edit(repository, parser, filter, normalizer, splitter, embedder)
			pipeline := mustPipeline(t, repository, parser, filter, normalizer, splitter, embedder)

			err := pipeline.Run(context.Background(), uuid.New())
			if !domain.IsCode(err, test.code) {
				t.Fatalf("error = %v, want code %s", err, test.code)
			}
			events, _ := repository.snapshot()
			if len(events) == 0 || events[len(events)-1] != test.last {
				t.Fatalf("pipeline continued after failure: %#v, want last %q", events, test.last)
			}
			if strings.Contains(err.Error(), componentFailure.Error()) {
				t.Fatalf("public error leaked internal detail: %v", err)
			}
		})
	}
}

func TestPipelinePreservesStableErrors(t *testing.T) {
	repository := &fakeRepository{source: domain.Upload{Markdown: []byte("content")}}
	stable := domain.NewError(domain.CodeInvalidEmbeddingResponse, "Embedding 响应无效", errors.New("private"))
	pipeline := mustPipeline(t, repository,
		&fakeParser{repository: repository},
		&fakeFilter{repository: repository},
		&fakeNormalizer{repository: repository},
		&fakeSplitter{repository: repository, chunks: []domain.Chunk{{EmbeddingContent: "content"}}},
		&fakeEmbedder{repository: repository, err: stable},
	)

	err := pipeline.Run(context.Background(), uuid.New())
	if !domain.IsCode(err, domain.CodeInvalidEmbeddingResponse) {
		t.Fatalf("error = %v", err)
	}
}

func TestPipelineRejectsInvalidEmbeddingCountAndDimensions(t *testing.T) {
	tests := []struct {
		name    string
		vectors [][]float64
	}{
		{name: "count", vectors: [][]float64{vectorWithFirstValue(1)}},
		{name: "dimension", vectors: [][]float64{vectorWithFirstValue(1), {1, 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{source: domain.Upload{Markdown: []byte("content")}}
			pipeline := mustPipeline(t, repository,
				&fakeParser{repository: repository},
				&fakeFilter{repository: repository},
				&fakeNormalizer{repository: repository},
				&fakeSplitter{repository: repository, chunks: []domain.Chunk{
					{EmbeddingContent: "first"},
					{EmbeddingContent: "second"},
				}},
				&fakeEmbedder{repository: repository, vectors: test.vectors},
			)

			err := pipeline.Run(context.Background(), uuid.New())
			if !domain.IsCode(err, domain.CodeInvalidEmbeddingResponse) {
				t.Fatalf("error = %v", err)
			}
			events, _ := repository.snapshot()
			if slicesContain(events, "activate") {
				t.Fatalf("invalid vectors were activated: %#v", events)
			}
		})
	}
}

func TestPipelineUsesPlainGoWithoutEinoCompose(t *testing.T) {
	source, err := os.ReadFile("pipeline.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "github.com/cloudwego/eino/compose") {
		t.Fatal("pipeline imports Eino compose")
	}
}

func mustPipeline(
	t *testing.T,
	repository Repository,
	parser DocumentParser,
	filter DocumentFilter,
	normalizer DocumentNormalizer,
	splitter ChunkSplitter,
	embedder TextEmbedder,
) *Pipeline {
	t.Helper()
	pipeline, err := NewPipeline(repository, parser, filter, normalizer, splitter, embedder)
	if err != nil {
		t.Fatal(err)
	}
	return pipeline
}

func vectorWithFirstValue(value float64) []float64 {
	vector := make([]float64, 1024)
	vector[0] = value
	return vector
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
