package splitter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
)

type Splitter struct {
	estimator TokenEstimator
}

type semanticUnit struct {
	content         string
	headingPath     []string
	startLine       int
	endLine         int
	split           bool
	allowTextSuffix bool
}

type chunkDraft struct {
	headingPath []string
	units       []semanticUnit
}

func New() *Splitter {
	return NewWithEstimator(ConservativeEstimator{})
}

func NewWithEstimator(estimator TokenEstimator) *Splitter {
	if estimator == nil {
		estimator = ConservativeEstimator{}
	}
	return &Splitter{estimator: estimator}
}

func (s *Splitter) Split(
	ctx context.Context,
	document domain.NormalizedDocument,
	config domain.ChunkConfig,
) ([]domain.Chunk, error) {
	if config.ChunkSize <= 0 || config.ChunkOverlap < 0 || config.ChunkOverlap >= config.ChunkSize {
		return nil, domain.NewError(
			domain.CodeDocumentSplitFailed,
			"文档切分配置无效",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	units := make([]semanticUnit, 0, len(document.Blocks))
	for _, block := range document.Blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(block.Content) == "" {
			continue
		}
		splitUnits, err := s.splitBlock(ctx, block, config.ChunkSize)
		if err != nil {
			return nil, err
		}
		units = append(units, splitUnits...)
	}

	drafts := s.pack(units, config.ChunkSize)
	chunks := make([]domain.Chunk, 0, len(drafts))
	for index, draft := range drafts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		finalDraft := draft
		if index > 0 &&
			config.ChunkOverlap > 0 &&
			equalHeadingPath(drafts[index-1].headingPath, draft.headingPath) {
			overlap := s.overlapUnits(drafts[index-1].units, config.ChunkOverlap)
			if len(overlap) > 0 {
				finalDraft.units = append(
					append(make([]semanticUnit, 0, len(overlap)+len(draft.units)), overlap...),
					draft.units...,
				)
			}
		}
		chunks = append(chunks, s.finalize(index, finalDraft))
	}
	return chunks, nil
}

func (s *Splitter) splitBlock(
	ctx context.Context,
	block domain.MarkdownBlock,
	chunkSize int,
) ([]semanticUnit, error) {
	base := semanticUnit{
		content:     block.Content,
		headingPath: append([]string(nil), block.HeadingPath...),
		startLine:   block.StartLine,
		endLine:     block.EndLine,
	}
	if s.estimator.Estimate(block.Content) <= chunkSize {
		return []semanticUnit{base}, nil
	}

	var contents []string
	var allowTextSuffix bool
	var err error
	switch block.Type {
	case domain.BlockTable:
		contents, err = s.splitTable(ctx, block.Content, chunkSize)
	case domain.BlockList:
		contents, err = s.splitList(ctx, block.Content, chunkSize)
	case domain.BlockCode:
		contents, err = s.splitCode(ctx, block.Content, chunkSize)
	default:
		contents, err = s.splitText(ctx, block.Content, chunkSize)
		allowTextSuffix = true
	}
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return []semanticUnit{base}, nil
	}

	result := make([]semanticUnit, 0, len(contents))
	for _, content := range contents {
		if strings.TrimSpace(content) == "" {
			continue
		}
		unit := base
		unit.content = content
		unit.split = true
		unit.allowTextSuffix = allowTextSuffix
		result = append(result, unit)
	}
	if len(result) == 0 {
		return []semanticUnit{base}, nil
	}
	return result, nil
}

func (s *Splitter) pack(units []semanticUnit, chunkSize int) []chunkDraft {
	drafts := make([]chunkDraft, 0, len(units))
	var current chunkDraft
	flush := func() {
		if len(current.units) == 0 {
			return
		}
		drafts = append(drafts, current)
		current = chunkDraft{}
	}

	for _, unit := range units {
		if len(current.units) == 0 {
			current = chunkDraft{
				headingPath: append([]string(nil), unit.headingPath...),
				units:       []semanticUnit{unit},
			}
			continue
		}
		if !equalHeadingPath(current.headingPath, unit.headingPath) {
			flush()
			current = chunkDraft{
				headingPath: append([]string(nil), unit.headingPath...),
				units:       []semanticUnit{unit},
			}
			continue
		}

		candidate := append(append([]semanticUnit(nil), current.units...), unit)
		if s.estimator.Estimate(joinUnitContent(candidate)) <= chunkSize {
			current.units = append(current.units, unit)
			continue
		}
		flush()
		current = chunkDraft{
			headingPath: append([]string(nil), unit.headingPath...),
			units:       []semanticUnit{unit},
		}
	}
	flush()
	return drafts
}

func (s *Splitter) overlapUnits(previous []semanticUnit, target int) []semanticUnit {
	if target <= 0 || len(previous) == 0 {
		return nil
	}

	var selected []semanticUnit
	tokens := 0
	for index := len(previous) - 1; index >= 0; index-- {
		unitTokens := s.estimator.Estimate(previous[index].content)
		if unitTokens == 0 {
			continue
		}
		if tokens+unitTokens > target {
			break
		}
		selected = append([]semanticUnit{previous[index]}, selected...)
		tokens += unitTokens
	}
	if len(selected) > 0 {
		return selected
	}

	last := previous[len(previous)-1]
	if !last.split || !last.allowTextSuffix {
		return nil
	}
	suffix := s.runeSafeSuffix(last.content, target)
	if suffix == "" {
		return nil
	}
	last.content = suffix
	return []semanticUnit{last}
}

func (s *Splitter) runeSafeSuffix(content string, target int) string {
	runes := []rune(content)
	for start := len(runes) - 1; start >= 0; start-- {
		suffix := strings.TrimSpace(string(runes[start:]))
		if suffix == "" {
			continue
		}
		if start == 0 ||
			(s.estimator.Estimate(suffix) >= target && hasOverlapSubstance(suffix)) {
			return suffix
		}
	}
	return ""
}

func hasOverlapSubstance(content string) bool {
	for _, current := range content {
		if !unicode.IsSpace(current) && !unicode.IsPunct(current) {
			return true
		}
	}
	return false
}

func (s *Splitter) splitText(ctx context.Context, content string, target int) ([]string, error) {
	remaining := strings.TrimSpace(content)
	parts := make([]string, 0, s.estimator.Estimate(content)/target+1)
	for remaining != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.estimator.Estimate(remaining) <= target {
			parts = append(parts, remaining)
			break
		}

		runes := []rune(remaining)
		firstTooLarge := sort.Search(len(runes), func(index int) bool {
			return s.estimator.Estimate(string(runes[:index+1])) > target
		})
		maximum := firstTooLarge
		if maximum == 0 {
			maximum = 1
		}
		cut := preferredTextCut(runes[:maximum])
		if cut <= 0 {
			cut = maximum
		}

		part := strings.TrimSpace(string(runes[:cut]))
		if part != "" {
			parts = append(parts, part)
		}
		remaining = strings.TrimSpace(string(runes[cut:]))
	}
	return parts, nil
}

func preferredTextCut(runes []rune) int {
	for index := len(runes) - 1; index > 0; index-- {
		if runes[index-1] == '\n' && runes[index] == '\n' {
			return index + 1
		}
	}
	if cut := lastRuneBoundary(runes, func(current rune) bool {
		return current == '\n'
	}); cut > 0 {
		return cut
	}
	if cut := lastRuneBoundary(runes, func(current rune) bool {
		return strings.ContainsRune("。！？", current)
	}); cut > 0 {
		return cut
	}
	if cut := lastRuneBoundary(runes, func(current rune) bool {
		return strings.ContainsRune("；，", current)
	}); cut > 0 {
		return cut
	}
	if cut := lastRuneBoundary(runes, func(current rune) bool {
		return strings.ContainsRune(".?!", current)
	}); cut > 0 {
		return cut
	}
	if cut := lastRuneBoundary(runes, unicode.IsSpace); cut > 0 {
		return cut
	}
	return len(runes)
}

func lastRuneBoundary(runes []rune, match func(rune) bool) int {
	for index := len(runes) - 1; index >= 0; index-- {
		if match(runes[index]) {
			return index + 1
		}
	}
	return 0
}

func (s *Splitter) splitTable(ctx context.Context, content string, target int) ([]string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) <= 2 {
		return []string{content}, nil
	}
	header := strings.Join(lines[:2], "\n")
	parts := make([]string, 0, len(lines)-2)
	var rows []string
	flush := func() {
		if len(rows) == 0 {
			return
		}
		parts = append(parts, header+"\n"+strings.Join(rows, "\n"))
		rows = nil
	}

	for _, row := range lines[2:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidateRows := append(append([]string(nil), rows...), row)
		candidate := header + "\n" + strings.Join(candidateRows, "\n")
		if len(rows) > 0 && s.estimator.Estimate(candidate) > target {
			flush()
		}
		rows = append(rows, row)
	}
	flush()
	return parts, nil
}

func (s *Splitter) splitList(ctx context.Context, content string, target int) ([]string, error) {
	lines := strings.Split(content, "\n")
	items := make([][]string, 0, len(lines))
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if isTopLevelListItem(line) {
			items = append(items, []string{line})
			continue
		}
		if len(items) == 0 {
			return []string{content}, nil
		}
		items[len(items)-1] = append(items[len(items)-1], line)
	}
	if len(items) <= 1 {
		return []string{content}, nil
	}

	parts := make([]string, 0, len(items))
	var current []string
	flush := func() {
		if len(current) == 0 {
			return
		}
		parts = append(parts, strings.Join(current, "\n"))
		current = nil
	}
	for _, itemLines := range items {
		item := strings.Join(itemLines, "\n")
		candidate := append(append([]string(nil), current...), item)
		if len(current) > 0 && s.estimator.Estimate(strings.Join(candidate, "\n")) > target {
			flush()
		}
		current = append(current, item)
	}
	flush()
	return parts, nil
}

func isTopLevelListItem(line string) bool {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return true
	}
	if line == "-" || line == "*" || line == "+" {
		return true
	}
	index := 0
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	return index > 0 &&
		index < len(line) &&
		line[index] == '.' &&
		(index+1 == len(line) || line[index+1] == ' ')
}

func (s *Splitter) splitCode(ctx context.Context, content string, target int) ([]string, error) {
	lines := strings.Split(content, "\n")
	parts := make([]string, 0, len(lines))
	var current []string
	flush := func() {
		if len(current) == 0 {
			return
		}
		parts = append(parts, strings.Join(current, "\n"))
		current = nil
	}

	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := append(append([]string(nil), current...), line)
		if strings.TrimSpace(strings.Join(current, "\n")) != "" &&
			s.estimator.Estimate(strings.Join(candidate, "\n")) > target {
			flush()
		}
		current = append(current, line)
	}
	flush()
	return parts, nil
}

func (s *Splitter) finalize(index int, draft chunkDraft) domain.Chunk {
	content := joinUnitContent(draft.units)
	headingPath := append([]string(nil), draft.headingPath...)
	embeddingContent := strings.TrimSpace(strings.Join(headingPath, " > ") + "\n\n" + content)
	sum := sha256.Sum256([]byte(embeddingContent))

	startLine, endLine := 0, 0
	for _, unit := range draft.units {
		if startLine == 0 || unit.startLine < startLine {
			startLine = unit.startLine
		}
		if unit.endLine > endLine {
			endLine = unit.endLine
		}
	}
	return domain.Chunk{
		ChunkIndex:       index,
		Content:          content,
		EmbeddingContent: embeddingContent,
		HeadingPath:      headingPath,
		StartLine:        startLine,
		EndLine:          endLine,
		EstimatedTokens:  s.estimator.Estimate(embeddingContent),
		ContentHash:      hex.EncodeToString(sum[:]),
	}
}

func joinUnitContent(units []semanticUnit) string {
	contents := make([]string, 0, len(units))
	for _, unit := range units {
		contents = append(contents, unit.content)
	}
	return strings.Join(contents, "\n\n")
}

func equalHeadingPath(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
