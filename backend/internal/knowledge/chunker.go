// 本文件把结构块拼成有上限的分块窗口，并把字节偏移换算成码点偏移。
//
// 三条设计决定，先说清楚为什么：
//
//  1. **块内容 = 原文的连续子串**。不重排、不剥离 Markdown 语法、不做空白归一化。
//     因此 `[]rune(text)[start:end]` 必然等于分块内容，引用能精确回指原文，
//     测试也能直接断言。代价是检索时正文里会带 "## " 这类标记——它对向量召回
//     几乎无影响，而"改写了原文却还声称偏移准确"才是真问题。
//
//  2. **对外偏移用码点（rune），不对外用字节**。中文一个字 3 字节，字节偏移会让
//     "第 100 个字符"变成"第 300 个字节"，人无法核对；码点在 Go（utf8.RuneCount）
//     与前端（Array.from）里口径一致。
//
//  3. **不设重叠（overlap）**。重叠能提升召回，但它是需要用评测数据支撑的调参项，
//     而 Phase 4 还没有检索。没有数据的旋钮只会让"偏移是否覆盖全文"这类断言变复杂。
package knowledge

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkOptions 控制切分粒度，单位是码点。
//
// Target 是"攒到这个长度就考虑换块"的目标值，Max 是硬上限，Min 是回退切点时
// 允许的最短长度。为什么三者都需要：只用 Max 会切出大量几十字的碎片；
// 只用 Target 则在超长段落（比如一整段没有标点的日志）上失去上限保护。
type ChunkOptions struct {
	Target int
	Max    int
	Min    int
}

// DefaultChunkOptions 是默认粒度。
//
// 600/900 是"一屏能读完、又不至于把两三个主题混在一起"的经验值：中文 600 字
// 大约 600 token，正好落在常见 embedding 模型的舒适区间；900 是硬上限，
// 防止代码块或长表格把单块撑到几千 token。
var DefaultChunkOptions = ChunkOptions{Target: 600, Max: 900, Min: 200}

// span 是切分过程中的中间结果，仍是字节偏移。
type span struct {
	start, end int
	heading    string
}

// Split 把结构块合并成最终分块，Seq 从 0 开始连续编号。
//
// 合并规则：
//   - 遇到标题块先封口，保证一个分块不会横跨两个章节；
//   - 未超过 Target 时，相邻段落/代码块合并进同一块；
//   - 单个块超过 Max 时单独切开，切点优先落在换行、句末标点、逗号空格上。
func Split(text string, blocks []Block, opts ChunkOptions) []Chunk {
	opts = normalizeOptions(opts)

	var spans []span
	heading := ""
	bufStart, bufEnd := -1, -1

	flush := func() {
		if bufStart < 0 {
			return
		}
		if s, e := trimSpace(text, bufStart, bufEnd); s < e {
			spans = append(spans, span{start: s, end: e, heading: heading})
		}
		bufStart, bufEnd = -1, -1
	}

	for _, b := range blocks {
		if b.Kind == BlockHeading {
			flush()
			heading = b.Heading
			// 标题块本身不 continue：它要和下面的正文合在一起，
			// 这样分块正文以标题开头，单独看也有上下文。
		}

		if utf8.RuneCountInString(text[b.Start:b.End]) > opts.Max {
			flush()
			for _, piece := range splitSpan(text, b.Start, b.End, opts) {
				if s, e := trimSpace(text, piece.start, piece.end); s < e {
					spans = append(spans, span{start: s, end: e, heading: heading})
				}
			}
			continue
		}

		if bufStart < 0 {
			bufStart, bufEnd = b.Start, b.End
			continue
		}
		if utf8.RuneCountInString(text[bufStart:b.End]) <= opts.Target {
			bufEnd = b.End
			continue
		}
		flush()
		bufStart, bufEnd = b.Start, b.End
	}
	flush()

	return toChunks(text, spans)
}

// toChunks 把字节区间换算成码点偏移并补上 token 估算值。
//
// 换算用游标累加而不是每个分块都从头数：分块按位置升序且不重叠，
// 一次遍历即可 O(n)；若每个块都 RuneCount(text[:start])，长文档会退化成 O(n×块数)。
func toChunks(text string, spans []span) []Chunk {
	chunks := make([]Chunk, 0, len(spans))
	cursor, runes := 0, 0
	for i, sp := range spans {
		// cursor 只会向前，sp.start 必然 >= cursor（分块由左到右生成）。
		runes += utf8.RuneCountInString(text[cursor:sp.start])
		cursor = sp.start
		startRune := runes
		runes += utf8.RuneCountInString(text[sp.start:sp.end])
		cursor = sp.end

		content := text[sp.start:sp.end]
		c := Chunk{
			Seq:         i,
			Content:     content,
			StartOffset: startRune,
			EndOffset:   runes,
			TokenCount:  estimateTokens(content),
		}
		if sp.heading != "" {
			h := sp.heading
			c.Heading = &h
		}
		chunks = append(chunks, c)
	}
	return chunks
}

// splitSpan 把一个超长块切成若干不超过 Max 的片段。
func splitSpan(text string, start, end int, opts ChunkOptions) []span {
	var out []span
	for start < end {
		cut := nextCut(text, start, end, opts)
		out = append(out, span{start: start, end: cut})
		start = cut
	}
	return out
}

// nextCut 返回从 start 开始这一片应该在哪个字节位置结束（不含）。
//
// 先扫到 Max 个码点，同时记住三种自然边界的位置；扫满之后从优到劣回退：
// 换行（段落/代码行边界）> 句末标点 > 空格与逗号顿号。
// 所有候选都必须不短于 Min，否则宁可硬切在 Max 处——否则一段没有标点的长文本
// 会被切成一堆很短的碎片。
func nextCut(text string, start, end int, opts ChunkOptions) int {
	pos := start
	count := 0
	lineCut, lineCount := -1, 0
	sentenceCut, sentenceCount := -1, 0
	clauseCut, clauseCount := -1, 0

	for count < opts.Max && pos < end {
		r, size := utf8.DecodeRuneInString(text[pos:end])
		pos += size
		count++
		switch {
		case r == '\n':
			lineCut, lineCount = pos, count
		case isSentenceEnd(r):
			sentenceCut, sentenceCount = pos, count
		case r == ' ' || r == '\t' || r == '，' || r == ',' || r == '、' || r == '；' || r == ';':
			clauseCut, clauseCount = pos, count
		}
	}

	if pos >= end {
		return end
	}
	for _, cand := range []struct{ cut, count int }{
		{lineCut, lineCount},
		{sentenceCut, sentenceCount},
		{clauseCut, clauseCount},
	} {
		if cand.cut > start && cand.count >= opts.Min {
			return cand.cut
		}
	}
	return pos
}

// isSentenceEnd 判断句末标点。中英文都收，全角标点必须显式列出：
// unicode.IsPunct 会把逗号、括号一起算进来，用它判断"句子结束"太宽。
func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?', ';':
		return true
	// 省略号与破折号后面通常也是自然停顿
	case '…':
		return true
	}
	return false
}

// trimSpace 收缩区间两端的空白（含换行与全角空格）。
//
// 分块边界落在空行上时，块首尾会带 "\n\n"；不裁掉的话，检索结果里每条都要
// 先看到一串空行，且 token 估算会偏高。裁剪后仍是原文子串，偏移同步修正。
func trimSpace(text string, start, end int) (int, int) {
	for start < end {
		r, size := utf8.DecodeRuneInString(text[start:end])
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	return start, end
}

// estimateTokens 粗略估算 token 数，供上下文预算与列表展示使用。
//
// 规则：CJK 等宽字符按 1 token/字，其余（拉丁字母、数字、空白、标点）按 4 字符/token。
// 这不是真实分词器，误差在 ±20% 量级；Phase 7 接入 embedding、Phase 19 做预算时
// 会换成模型自带的 tokenizer。现在引入一个分词库只为"显示个数字"并不划算。
func estimateTokens(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	wide, narrow := 0, 0
	for _, r := range s {
		if r >= 0x2E80 {
			wide++
			continue
		}
		narrow++
	}
	tokens := wide + (narrow+3)/4
	if tokens == 0 {
		return 1
	}
	return tokens
}

// normalizeOptions 兜住非法配置：调用方（或未来的配置项）传了 0 或颠倒的值时，
// 用默认值补齐，而不是让切分循环因为 Min > Max 陷入死循环。
func normalizeOptions(opts ChunkOptions) ChunkOptions {
	d := DefaultChunkOptions
	if opts.Max <= 0 {
		opts.Max = d.Max
	}
	if opts.Target <= 0 || opts.Target > opts.Max {
		opts.Target = opts.Max
	}
	if opts.Min < 0 || opts.Min > opts.Target {
		opts.Min = opts.Target / 3
	}
	return opts
}
