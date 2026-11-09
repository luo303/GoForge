package knowledge

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func mustParse(t *testing.T, mime, text string) []Block {
	t.Helper()
	blocks, err := Parse(mime, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return blocks
}

// assertChunkInvariants 检查分块结果最要紧的三条不变量：
// 偏移能切回原文、每块不超过上限、块与块之间的空隙只有空白。
func assertChunkInvariants(t *testing.T, text string, chunks []Chunk, max int) {
	t.Helper()
	runes := []rune(text)
	prevEnd := 0
	for i, c := range chunks {
		if c.Seq != i {
			t.Errorf("chunk %d has Seq %d, want %d", i, c.Seq, i)
		}
		if c.StartOffset < 0 || c.EndOffset < c.StartOffset || c.EndOffset > len(runes) {
			t.Fatalf("chunk %d has invalid offsets [%d,%d) of %d", i, c.StartOffset, c.EndOffset, len(runes))
		}
		// 最核心的一条：用码点偏移切原文，必须逐字等于分块内容。
		if got := string(runes[c.StartOffset:c.EndOffset]); got != c.Content {
			t.Errorf("chunk %d content mismatch\n got %q\nwant %q", i, got, c.Content)
		}
		if n := utf8.RuneCountInString(c.Content); n > max {
			t.Errorf("chunk %d has %d runes, exceeds max %d", i, n, max)
		}
		if i > 0 {
			gap := string(runes[prevEnd:c.StartOffset])
			if strings.TrimSpace(gap) != "" {
				t.Errorf("chunk %d dropped non-space text %q", i, gap)
			}
		}
		prevEnd = c.EndOffset
	}
	if tail := string(runes[prevEnd:]); strings.TrimSpace(tail) != "" {
		t.Errorf("tail after last chunk not covered: %q", tail)
	}
}

func TestSplitSmallDocumentIsSingleChunk(t *testing.T) {
	text := "# 标题\n\n第一段内容。\n"
	chunks := Split(text, mustParse(t, MIMEMarkdown, text), DefaultChunkOptions)

	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %+v", len(chunks), chunks)
	}
	if chunks[0].Content != "# 标题\n\n第一段内容。" {
		t.Fatalf("content = %q", chunks[0].Content)
	}
	if chunks[0].Heading == nil || *chunks[0].Heading != "标题" {
		t.Fatalf("heading = %v, want 标题", chunks[0].Heading)
	}
	if chunks[0].StartOffset != 0 || chunks[0].EndOffset != 12 {
		t.Fatalf("offsets = [%d,%d), want [0,12)", chunks[0].StartOffset, chunks[0].EndOffset)
	}
	if chunks[0].TokenCount <= 0 {
		t.Fatalf("token count should be positive, got %d", chunks[0].TokenCount)
	}
	assertChunkInvariants(t, text, chunks, DefaultChunkOptions.Max)
}

func TestSplitDoesNotMergeAcrossHeadings(t *testing.T) {
	text := "# 甲\n\n甲的内容。\n\n# 乙\n\n乙的内容。\n"
	chunks := Split(text, mustParse(t, MIMEMarkdown, text), DefaultChunkOptions)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(chunks), chunks)
	}
	// 标题必须跟着自己的正文，而不是被合并成一个横跨两章的块。
	if !strings.HasPrefix(chunks[0].Content, "# 甲") || !strings.HasPrefix(chunks[1].Content, "# 乙") {
		t.Fatalf("chunks not aligned to headings: %q / %q", chunks[0].Content, chunks[1].Content)
	}
	if *chunks[0].Heading != "甲" || *chunks[1].Heading != "乙" {
		t.Fatalf("headings = %q / %q", *chunks[0].Heading, *chunks[1].Heading)
	}
}

func TestSplitKeepsTextBeforeFirstHeading(t *testing.T) {
	text := "前言。\n\n# 正文\n\n正文内容。\n"
	chunks := Split(text, mustParse(t, MIMEMarkdown, text), DefaultChunkOptions)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != nil {
		t.Fatalf("heading before any heading block should be nil, got %q", *chunks[0].Heading)
	}
	if *chunks[1].Heading != "正文" {
		t.Fatalf("second chunk heading = %q", *chunks[1].Heading)
	}
}

func TestSplitLongParagraphRespectsMax(t *testing.T) {
	sentence := "这是一个用于测试切分边界的句子。"
	text := strings.Repeat(sentence, 120) // 约 1900 个码点

	opts := DefaultChunkOptions
	chunks := Split(text, mustParse(t, MIMEMarkdown, text), opts)
	assertChunkInvariants(t, text, chunks, opts.Max)

	if len(chunks) < 2 {
		t.Fatalf("long paragraph should be split, got %d chunks", len(chunks))
	}
	// 中文句子有句号，除最后一块外都应该在句号处收尾，而不是句中硬切。
	for i, c := range chunks[:len(chunks)-1] {
		if !strings.HasSuffix(c.Content, "。") {
			t.Errorf("chunk %d should end at sentence boundary, ends with %q", i, lastRunes(c.Content, 5))
		}
	}
}

func TestSplitHardCutsUnpunctuatedText(t *testing.T) {
	// 没有任何标点与换行：只能硬切，但硬切点必须落在码点边界上。
	text := strings.Repeat("a", 2000)
	opts := ChunkOptions{Target: 100, Max: 200, Min: 50}
	chunks := Split(text, mustParse(t, MIMEText, text), opts)

	assertChunkInvariants(t, text, chunks, opts.Max)
	if len(chunks) != 10 {
		t.Fatalf("got %d chunks, want 10 (2000/200)", len(chunks))
	}
}

func TestSplitCodeBlockAtLineBoundaries(t *testing.T) {
	var b strings.Builder
	b.WriteString("```go\n")
	for i := 0; i < 60; i++ {
		b.WriteString("func handler() { return nil } // line ")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	text := b.String()

	opts := ChunkOptions{Target: 200, Max: 300, Min: 60}
	chunks := Split(text, mustParse(t, MIMEMarkdown, text), opts)
	assertChunkInvariants(t, text, chunks, opts.Max)

	if len(chunks) < 3 {
		t.Fatalf("code block should be split into several chunks, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0].Content, "```go") {
		t.Errorf("first chunk should keep the opening fence, got %q", firstRunes(chunks[0].Content, 10))
	}
	// 代码块必须在换行处切：每一块的结束位置后面，原文紧跟的必须是换行或文末。
	runes := []rune(text)
	for i, c := range chunks {
		if c.EndOffset >= len(runes) {
			continue
		}
		if next := runes[c.EndOffset]; next != '\n' {
			t.Errorf("chunk %d ends mid-line: %q (next rune %q)", i, lastRunes(c.Content, 20), next)
		}
	}
	for i, c := range chunks[1:] {
		if prev := runes[c.StartOffset-1]; prev != '\n' {
			t.Errorf("chunk %d starts mid-line: %q (prev rune %q)", i+1, firstRunes(c.Content, 20), prev)
		}
	}
}

func TestSplitOffsetsAcrossWideCharacters(t *testing.T) {
	// emoji 在 Go 与 JS 里都是 1 个码点，但 UTF-16 长度是 2、UTF-8 长度是 4。
	// 这里确认对外偏移按码点走，避免前端高亮错位。
	text := "😀 开头\n\n中文🙂正文。\n"
	chunks := Split(text, mustParse(t, MIMEMarkdown, text), DefaultChunkOptions)

	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	// 末尾换行会在裁剪时去掉，所以比较的是裁剪后正文的码点数。
	trimmed := strings.TrimSpace(text)
	if chunks[0].EndOffset != utf8.RuneCountInString(trimmed) {
		t.Fatalf("end offset = %d, want %d", chunks[0].EndOffset, utf8.RuneCountInString(trimmed))
	}
	if got := chunks[0].EndOffset - chunks[0].StartOffset; got != utf8.RuneCountInString(chunks[0].Content) {
		t.Fatalf("offset width = %d, content runes = %d", got, utf8.RuneCountInString(chunks[0].Content))
	}
	assertChunkInvariants(t, text, chunks, DefaultChunkOptions.Max)
}

func TestSplitEmptyBlocks(t *testing.T) {
	chunks := Split("", nil, DefaultChunkOptions)
	if len(chunks) != 0 {
		t.Fatalf("got %d chunks for empty document, want 0", len(chunks))
	}
}

func TestNormalizeOptions(t *testing.T) {
	opts := normalizeOptions(ChunkOptions{})
	if opts.Max != DefaultChunkOptions.Max || opts.Target > opts.Max || opts.Min > opts.Target {
		t.Fatalf("zero options normalized to %+v", opts)
	}

	opts = normalizeOptions(ChunkOptions{Target: 50, Max: 10, Min: 999})
	if opts.Target > opts.Max || opts.Min > opts.Target {
		t.Fatalf("inconsistent options normalized to %+v", opts)
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// TestEstimateTokens 守住"宽字符按 1 token"这个量级，避免以后误改成 len(s)。
func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens("中文四个字"); got != 5 {
		t.Errorf("estimateTokens(中文四个字) = %d, want 5", got)
	}
	if got := estimateTokens("abcd"); got != 1 {
		t.Errorf("estimateTokens(abcd) = %d, want 1", got)
	}
	if got := estimateTokens("   "); got != 0 {
		t.Errorf("estimateTokens(spaces) = %d, want 0", got)
	}
}

// TestTrimSpaceKeepsOffsetsAligned 确认裁剪后偏移仍指向原文（不是裁剪后的副本）。
func TestTrimSpaceKeepsOffsetsAligned(t *testing.T) {
	text := "  \n内容\t \n"
	start, end := trimSpace(text, 0, len(text))
	if got := text[start:end]; got != "内容" {
		t.Fatalf("trimmed = %q, want 内容", got)
	}
	if !unicode.IsLetter(rune(text[start])) {
		t.Fatal("start should point at the first non-space rune")
	}
}
