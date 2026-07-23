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
	allowTextSuffix bool
}

type blockPart struct {
	content   string
	startLine int
	endLine   int
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
		splitUnits, err := s.splitBlock(
			ctx,
			block,
			s.contentBudget(block.HeadingPath, config.ChunkSize),
		)
		if err != nil {
			return nil, err
		}
		units = append(units, splitUnits...)
	}

	drafts := s.pack(units, config.ChunkSize, config.ChunkOverlap)
	chunks := make([]domain.Chunk, 0, len(drafts))
	for index, draft := range drafts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunks = append(chunks, s.finalize(index, draft))
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

	var parts []blockPart
	var allowTextSuffix bool
	var err error
	switch block.Type {
	case domain.BlockTable:
		parts, err = s.splitTable(ctx, block, chunkSize)
	case domain.BlockList:
		parts, err = s.splitList(ctx, block, chunkSize)
	case domain.BlockCode:
		parts, err = s.splitCode(ctx, block, chunkSize)
	default:
		var contents []string
		contents, err = s.splitText(ctx, block.Content, chunkSize)
		allowTextSuffix = true
		parts = make([]blockPart, 0, len(contents))
		for _, content := range contents {
			parts = append(parts, blockPart{
				content:   content,
				startLine: block.StartLine,
				endLine:   block.EndLine,
			})
		}
	}
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return []semanticUnit{base}, nil
	}

	result := make([]semanticUnit, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.content) == "" {
			continue
		}
		unit := base
		unit.content = part.content
		unit.startLine = part.startLine
		unit.endLine = part.endLine
		unit.allowTextSuffix = allowTextSuffix
		result = append(result, unit)
	}
	if len(result) == 0 {
		return []semanticUnit{base}, nil
	}
	return result, nil
}

func (s *Splitter) pack(units []semanticUnit, chunkSize, overlapTarget int) []chunkDraft {
	drafts := make([]chunkDraft, 0, len(units))
	for groupStart := 0; groupStart < len(units); {
		groupEnd := groupStart + 1
		for groupEnd < len(units) &&
			equalHeadingPath(units[groupStart].headingPath, units[groupEnd].headingPath) {
			groupEnd++
		}
		drafts = append(
			drafts,
			s.packHeadingGroup(units[groupStart:groupEnd], chunkSize, overlapTarget)...,
		)
		groupStart = groupEnd
	}
	return drafts
}

func (s *Splitter) packHeadingGroup(
	units []semanticUnit,
	chunkSize, overlapTarget int,
) []chunkDraft {
	drafts := make([]chunkDraft, 0, len(units))
	var previous []semanticUnit
	contentBudget := s.contentBudget(units[0].headingPath, chunkSize)

	for next := 0; next < len(units); {
		fresh := units[next]
		overlap := s.overlapForNext(previous, fresh, contentBudget, overlapTarget)
		current := append(append([]semanticUnit(nil), overlap...), fresh)
		next++

		for next < len(units) {
			candidate := append(append([]semanticUnit(nil), current...), units[next])
			if s.estimator.Estimate(joinUnitContent(candidate)) > contentBudget {
				break
			}
			current = append(current, units[next])
			next++
		}

		drafts = append(drafts, chunkDraft{
			headingPath: append([]string(nil), fresh.headingPath...),
			units:       current,
		})
		previous = current
	}
	return drafts
}

func (s *Splitter) contentBudget(headingPath []string, chunkSize int) int {
	headingCost := s.estimator.Estimate(strings.Join(headingPath, " > "))
	return max(1, chunkSize-headingCost)
}

func (s *Splitter) overlapForNext(
	previous []semanticUnit,
	fresh semanticUnit,
	contentBudget, target int,
) []semanticUnit {
	if target <= 0 || len(previous) == 0 ||
		s.estimator.Estimate(fresh.content) >= contentBudget {
		return nil
	}

	selected := s.trailingOverlap(previous, target)
	for len(selected) > 1 &&
		s.estimator.Estimate(joinUnitContent(append(
			append([]semanticUnit(nil), selected...),
			fresh,
		))) > contentBudget {
		selected = selected[1:]
	}
	if len(selected) == 0 {
		return nil
	}
	if s.estimator.Estimate(joinUnitContent(append(
		append([]semanticUnit(nil), selected...),
		fresh,
	))) <= contentBudget {
		return selected
	}

	last := selected[len(selected)-1]
	if !last.allowTextSuffix {
		return nil
	}
	available := contentBudget - s.estimator.Estimate(fresh.content)
	if available <= 0 {
		return nil
	}
	suffix := s.boundedTextSuffix(last.content, min(target, available))
	if suffix == "" {
		return nil
	}
	last.content = suffix
	if s.estimator.Estimate(joinUnitContent([]semanticUnit{last, fresh})) > contentBudget {
		return nil
	}
	return []semanticUnit{last}
}

func (s *Splitter) trailingOverlap(previous []semanticUnit, target int) []semanticUnit {
	var selected []semanticUnit
	for index := len(previous) - 1; index >= 0; index-- {
		candidate := append([]semanticUnit{previous[index]}, selected...)
		if s.estimator.Estimate(joinUnitContent(candidate)) > target {
			break
		}
		selected = candidate
	}
	if len(selected) > 0 {
		return selected
	}

	last := previous[len(previous)-1]
	if last.allowTextSuffix {
		suffix := s.boundedTextSuffix(last.content, target)
		if suffix == "" {
			return nil
		}
		last.content = suffix
		return []semanticUnit{last}
	}
	return []semanticUnit{last}
}

func (s *Splitter) boundedTextSuffix(content string, target int) string {
	runes := []rune(content)
	if len(runes) == 0 || target <= 0 {
		return ""
	}

	start := len(runes)
	if weighted, ok := s.estimator.(interface {
		runeWeight(rune) int
		weightScale() int
	}); ok {
		budget := target * weighted.weightScale()
		weight := 0
		for start > 0 {
			nextWeight := weighted.runeWeight(runes[start-1])
			if weight+nextWeight > budget {
				break
			}
			start--
			weight += nextWeight
		}
	} else {
		start = sort.Search(len(runes), func(index int) bool {
			return s.estimator.Estimate(strings.TrimSpace(string(runes[index:]))) <= target
		})
		if start == len(runes) {
			return ""
		}
	}

	best := strings.TrimSpace(string(runes[start:]))
	if !hasOverlapSubstance(best) {
		return ""
	}
	return best
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
	runes := []rune(strings.TrimSpace(content))
	if weighted, ok := s.estimator.(interface {
		runeWeight(rune) int
		weightScale() int
	}); ok {
		return splitWeightedText(ctx, runes, target, weighted)
	}

	parts := make([]string, 0)
	for start := 0; start < len(runes); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		maximum := s.genericTextWindowEnd(runes, start, target)
		cut := start + preferredTextCut(runes[start:maximum])
		part := strings.TrimSpace(string(runes[start:cut]))
		if part != "" {
			parts = append(parts, part)
		}
		start = skipRuneWhitespace(runes, cut)
	}
	return parts, nil
}

func splitWeightedText(
	ctx context.Context,
	runes []rune,
	target int,
	estimator interface {
		runeWeight(rune) int
		weightScale() int
	},
) ([]string, error) {
	parts := make([]string, 0)
	start, windowEnd, windowWeight := 0, 0, 0
	budget := target * estimator.weightScale()

	for start < len(runes) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if windowEnd < start {
			windowEnd = start
			windowWeight = 0
		}
		for windowEnd < len(runes) {
			nextWeight := estimator.runeWeight(runes[windowEnd])
			if windowEnd > start && windowWeight+nextWeight > budget {
				break
			}
			windowWeight += nextWeight
			windowEnd++
			if windowWeight > budget {
				break
			}
		}
		if windowEnd == start {
			windowEnd++
		}

		cut := start + preferredTextCut(runes[start:windowEnd])
		part := strings.TrimSpace(string(runes[start:cut]))
		if part != "" {
			parts = append(parts, part)
		}

		nextStart := skipRuneWhitespace(runes, cut)
		if nextStart <= windowEnd {
			for index := start; index < nextStart; index++ {
				windowWeight -= estimator.runeWeight(runes[index])
			}
		} else {
			windowEnd = nextStart
			windowWeight = 0
		}
		start = nextStart
	}
	return parts, nil
}

func (s *Splitter) genericTextWindowEnd(runes []rune, start, target int) int {
	knownFit := start
	step := 1
	for {
		candidateEnd := min(start+step, len(runes))
		if s.estimator.Estimate(string(runes[start:candidateEnd])) > target {
			if knownFit == start {
				return start + 1
			}
			low, high := knownFit+1, candidateEnd
			for low < high {
				middle := low + (high-low)/2
				if s.estimator.Estimate(string(runes[start:middle])) <= target {
					low = middle + 1
				} else {
					high = middle
				}
			}
			return low - 1
		}
		knownFit = candidateEnd
		if candidateEnd == len(runes) {
			return candidateEnd
		}
		step *= 2
	}
}

func skipRuneWhitespace(runes []rune, start int) int {
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	return start
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

func (s *Splitter) splitTable(
	ctx context.Context,
	block domain.MarkdownBlock,
	target int,
) ([]blockPart, error) {
	lines := strings.Split(block.Content, "\n")
	if len(lines) <= 2 {
		return []blockPart{wholeBlockPart(block)}, nil
	}
	header := strings.Join(lines[:2], "\n")
	parts := make([]blockPart, 0, len(lines)-2)
	type tableRow struct {
		content    string
		lineOffset int
	}
	var rows []tableRow
	flush := func() {
		if len(rows) == 0 {
			return
		}
		contents := make([]string, 0, len(rows))
		for _, row := range rows {
			contents = append(contents, row.content)
		}
		parts = append(parts, blockPart{
			content:   header + "\n" + strings.Join(contents, "\n"),
			startLine: sourceLineAt(block, 0),
			endLine:   sourceLineAt(block, rows[len(rows)-1].lineOffset),
		})
		rows = nil
	}

	for index, row := range lines[2:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidateContents := make([]string, 0, len(rows)+1)
		for _, current := range rows {
			candidateContents = append(candidateContents, current.content)
		}
		candidateContents = append(candidateContents, row)
		candidate := header + "\n" + strings.Join(candidateContents, "\n")
		if len(rows) > 0 && s.estimator.Estimate(candidate) > target {
			flush()
		}
		rows = append(rows, tableRow{content: row, lineOffset: index + 2})
	}
	flush()
	return parts, nil
}

func (s *Splitter) splitList(
	ctx context.Context,
	block domain.MarkdownBlock,
	target int,
) ([]blockPart, error) {
	lines := strings.Split(block.Content, "\n")
	type listItem struct {
		lines     []string
		startLine int
		endLine   int
	}
	items := make([]listItem, 0, len(lines))
	for index, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if isTopLevelListItem(line) {
			items = append(items, listItem{
				lines:     []string{line},
				startLine: sourceLineAt(block, index),
				endLine:   sourceLineAt(block, index),
			})
			continue
		}
		if len(items) == 0 {
			return []blockPart{wholeBlockPart(block)}, nil
		}
		items[len(items)-1].lines = append(items[len(items)-1].lines, line)
		items[len(items)-1].endLine = sourceLineAt(block, index)
	}
	if len(items) <= 1 {
		return []blockPart{wholeBlockPart(block)}, nil
	}
	if strings.TrimSpace(block.RawContent) != "" {
		if spans, ok := rawListItemSpans(block, len(items)); ok {
			for index := range items {
				items[index].startLine = spans[index][0]
				items[index].endLine = spans[index][1]
			}
		} else {
			for index := range items {
				items[index].startLine = block.StartLine
				items[index].endLine = block.EndLine
			}
		}
	}

	parts := make([]blockPart, 0, len(items))
	var current []listItem
	flush := func() {
		if len(current) == 0 {
			return
		}
		contents := make([]string, 0, len(current))
		for _, item := range current {
			contents = append(contents, strings.Join(item.lines, "\n"))
		}
		parts = append(parts, blockPart{
			content:   strings.Join(contents, "\n"),
			startLine: current[0].startLine,
			endLine:   current[len(current)-1].endLine,
		})
		current = nil
	}
	for _, item := range items {
		candidateContents := make([]string, 0, len(current)+1)
		for _, existing := range current {
			candidateContents = append(candidateContents, strings.Join(existing.lines, "\n"))
		}
		candidateContents = append(candidateContents, strings.Join(item.lines, "\n"))
		if len(current) > 0 &&
			s.estimator.Estimate(strings.Join(candidateContents, "\n")) > target {
			flush()
		}
		current = append(current, item)
	}
	flush()
	return parts, nil
}

func isTopLevelListItem(line string) bool {
	return isListItemMarker(line)
}

func isListItemMarker(line string) bool {
	if line == "" {
		return false
	}
	markerEnd := 0
	switch line[0] {
	case '-', '*', '+':
		markerEnd = 1
	default:
		for markerEnd < len(line) &&
			markerEnd < 9 &&
			line[markerEnd] >= '0' &&
			line[markerEnd] <= '9' {
			markerEnd++
		}
		if markerEnd == 0 ||
			markerEnd >= len(line) ||
			(line[markerEnd] != '.' && line[markerEnd] != ')') {
			return false
		}
		markerEnd++
	}
	return markerEnd == len(line) ||
		line[markerEnd] == ' ' ||
		line[markerEnd] == '\t'
}

func rawListItemSpans(block domain.MarkdownBlock, want int) ([][2]int, bool) {
	if strings.TrimSpace(block.RawContent) == "" || want <= 0 {
		return nil, false
	}
	rawLines := strings.Split(block.RawContent, "\n")
	type marker struct {
		offset int
		indent int
	}
	markers := make([]marker, 0, want)
	minimumIndent := -1
	for offset, line := range rawLines {
		indent, content := sourceIndent(line)
		if !isListItemMarker(content) {
			continue
		}
		markers = append(markers, marker{offset: offset, indent: indent})
		if minimumIndent < 0 || indent < minimumIndent {
			minimumIndent = indent
		}
	}

	starts := make([]int, 0, want)
	for _, current := range markers {
		if current.indent == minimumIndent {
			starts = append(starts, current.offset)
		}
	}
	if len(starts) != want {
		return nil, false
	}

	spans := make([][2]int, 0, want)
	for index, startOffset := range starts {
		endOffset := len(rawLines) - 1
		if index+1 < len(starts) {
			endOffset = starts[index+1] - 1
		}
		for endOffset > startOffset && strings.TrimSpace(rawLines[endOffset]) == "" {
			endOffset--
		}
		spans = append(spans, [2]int{
			sourceLineAt(block, startOffset),
			sourceLineAt(block, endOffset),
		})
	}
	return spans, true
}

func sourceIndent(line string) (int, string) {
	index, indent := 0, 0
	for index < len(line) {
		switch line[index] {
		case ' ':
			indent++
			index++
		case '\t':
			indent += 4
			index++
		default:
			return indent, line[index:]
		}
	}
	return indent, ""
}

func (s *Splitter) splitCode(
	ctx context.Context,
	block domain.MarkdownBlock,
	target int,
) ([]blockPart, error) {
	lines := strings.Split(block.Content, "\n")
	contentStart, contentEnd := codeContentSourceRange(block)
	type codeLine struct {
		content    string
		sourceLine int
	}
	parts := make([]blockPart, 0, len(lines))
	var current []codeLine
	flush := func() {
		if len(current) == 0 {
			return
		}
		contents := make([]string, 0, len(current))
		for _, line := range current {
			contents = append(contents, line.content)
		}
		parts = append(parts, blockPart{
			content:   strings.Join(contents, "\n"),
			startLine: current[0].sourceLine,
			endLine:   current[len(current)-1].sourceLine,
		})
		current = nil
	}

	for index, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sourceLine := contentStart + index
		if contentEnd >= contentStart && sourceLine > contentEnd {
			sourceLine = contentEnd
		}
		candidateContents := make([]string, 0, len(current)+1)
		for _, existing := range current {
			candidateContents = append(candidateContents, existing.content)
		}
		candidateContents = append(candidateContents, line)
		if len(current) > 0 &&
			strings.TrimSpace(strings.Join(candidateContents[:len(candidateContents)-1], "\n")) != "" &&
			s.estimator.Estimate(strings.Join(candidateContents, "\n")) > target {
			flush()
		}
		current = append(current, codeLine{content: line, sourceLine: sourceLine})
	}
	flush()
	return parts, nil
}

func codeContentSourceRange(block domain.MarkdownBlock) (int, int) {
	startLine, endLine := block.StartLine, block.EndLine
	rawLines := strings.Split(block.RawContent, "\n")
	if len(rawLines) < 2 {
		return startLine, endLine
	}
	marker, length, ok := codeFence(rawLines[0])
	if !ok {
		return startLine, endLine
	}
	if startLine > 0 {
		startLine++
	}
	if endLine > 0 && isClosingCodeFence(rawLines[len(rawLines)-1], marker, length) {
		endLine--
	}
	return startLine, endLine
}

func codeFence(line string) (byte, int, bool) {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	if index >= len(line) || (line[index] != '`' && line[index] != '~') {
		return 0, 0, false
	}
	marker := line[index]
	start := index
	for index < len(line) && line[index] == marker {
		index++
	}
	length := index - start
	return marker, length, length >= 3
}

func isClosingCodeFence(line string, marker byte, openingLength int) bool {
	index := 0
	for index < len(line) && index < 3 && line[index] == ' ' {
		index++
	}
	start := index
	for index < len(line) && line[index] == marker {
		index++
	}
	return index-start >= openingLength && strings.TrimSpace(line[index:]) == ""
}

func wholeBlockPart(block domain.MarkdownBlock) blockPart {
	return blockPart{
		content:   block.Content,
		startLine: block.StartLine,
		endLine:   block.EndLine,
	}
}

func sourceLineAt(block domain.MarkdownBlock, offset int) int {
	if block.StartLine <= 0 {
		return block.StartLine
	}
	line := block.StartLine + offset
	if block.EndLine >= block.StartLine && line > block.EndLine {
		return block.EndLine
	}
	return line
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
