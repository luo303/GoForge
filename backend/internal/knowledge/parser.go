// 本文件把原文切成"结构块"（标题 / 段落 / 代码块），每个块只记区间，不改写文本。
//
// 为什么不用现成的 Markdown 库（goldmark 等）：本阶段需要的是**边界**，不是
// 语法树。所有块都是原文的连续子串，于是分块可以精确映射回原文件，引用能高亮，
// 测试也能直接断言 `text[start:end]`。换成 AST 解析器会把"第 N 个字符"变成
// "第 N 个节点的第 M 个偏移"，为了同样的定位能力要写更多换算代码。
//
// 边界精度取舍：行级扫描认识 ATX 标题（#）、围栏代码块（``` / ~~~）、空行分段。
// 表格、嵌套列表、行内代码都不特殊处理——它们会自然落进段落块里，
// 对"按段落切分 + 保留偏移"这个目标没有影响。
package knowledge

import (
	"errors"
	"path/filepath"
	"strings"
)

// 支持的 MIME 类型。application/pdf 与 text/html 暂不支持。
const (
	MIMEMarkdown = "text/markdown"
	MIMEText     = "text/plain"
)

// ErrUnsupportedType 表示文件扩展名不在本阶段支持范围内。
var ErrUnsupportedType = errors.New("unsupported document type")

// BlockKind 是结构块的类型。
type BlockKind int

const (
	// BlockParagraph 是普通正文段落（也可能是列表项、表格行——它们不单独成块）。
	BlockParagraph BlockKind = iota
	// BlockHeading 是 ATX 标题行。
	BlockHeading
	// BlockCode 是围栏代码块，包含开头与结尾的围栏行。
	BlockCode
)

// Block 是原文中的一个连续区间。
//
// Start / End 是**字节**偏移（左闭右开），因为 Go 的 string 切片与所有扫描函数
// （strings.IndexByte、utf8.DecodeRuneInString）都按字节工作。切分完成后再统一
// 换算成码点偏移对外暴露，换算只做一次，见 chunker.go。
type Block struct {
	Kind    BlockKind
	Level   int    // 标题层级 1–6；非标题为 0
	Heading string // 标题文本（去掉 # 与收尾的 #）；非标题为空
	Start   int
	End     int
}

// MIMEByFilename 按扩展名判断 MIME。
//
// 不用 http.DetectContentType 嗅探：它只看前 512 字节，中文 Markdown 很容易被
// 判成 text/plain，而"扩展名是 .md 但内容像二进制"这种异常我们宁可让 UTF-8
// 校验去挡，也不靠猜测。
func MIMEByFilename(name string) (string, bool) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return MIMEMarkdown, true
	case ".txt", ".text":
		return MIMEText, true
	default:
		return "", false
	}
}

// Parse 按 MIME 选择扫描规则，返回按位置升序的结构块。
//
// 空文档返回空切片且不报错："内容为空"是业务校验（service 层），
// 解析器只负责翻译结构。
func Parse(mime, text string) ([]Block, error) {
	switch mime {
	case MIMEMarkdown:
		return parseBlocks(text, true), nil
	case MIMEText:
		return parseBlocks(text, false), nil
	default:
		return nil, ErrUnsupportedType
	}
}

// parseBlocks 逐行扫描原文。
//
// markdown 为 false 时只认空行分段：纯文本里的 "# 标题" 就是普通一行，
// 否则用户上传的日志、笔记里以 # 开头的行会被误当成标题。
func parseBlocks(text string, markdown bool) []Block {
	var blocks []Block

	// 段落跨行累积：单换行不分段（Markdown 里软换行属于同一段），
	// 只有空行才结束一段。-1 表示当前没有正在累积的段落。
	paraStart, paraEnd := -1, -1
	flushParagraph := func() {
		if paraStart >= 0 {
			blocks = append(blocks, Block{Kind: BlockParagraph, Start: paraStart, End: paraEnd})
			paraStart, paraEnd = -1, -1
		}
	}

	pos := 0
	for pos < len(text) {
		lineEnd, next := lineBounds(text, pos)
		// 去掉行尾的 \r：Windows 换行不应影响结构判断，
		// 但 Start / End 仍然落在原始字节上，块内容里保留 \r。
		line := strings.TrimRight(text[pos:lineEnd], "\r")
		trimmed := strings.TrimLeft(line, " \t")

		if strings.TrimSpace(trimmed) == "" {
			flushParagraph()
			pos = next
			continue
		}

		if markdown && isFenceLine(trimmed) {
			flushParagraph()
			blockStart := pos
			closed, codeEnd := scanFence(text, next, trimmed[:3])
			pos = codeEnd
			if !closed {
				// 没有收尾围栏：按 CommonMark 的容错习惯，围栏一直延续到文末。
				pos = len(text)
			}
			blocks = append(blocks, Block{Kind: BlockCode, Start: blockStart, End: codeEnd})
			continue
		}

		if markdown {
			if level, title, ok := parseHeading(trimmed); ok {
				flushParagraph()
				blocks = append(blocks, Block{
					Kind:    BlockHeading,
					Level:   level,
					Heading: title,
					Start:   pos,
					End:     pos + len(line),
				})
				pos = next
				continue
			}
		}

		if paraStart < 0 {
			paraStart = pos
		}
		paraEnd = pos + len(line)
		pos = next
	}
	flushParagraph()

	return blocks
}

// lineBounds 返回从 start 开始这一行的结束位置（不含换行符）与下一行起点。
// 文件末尾没有换行符时两者都等于 len(text)。
func lineBounds(text string, start int) (end, next int) {
	idx := strings.IndexByte(text[start:], '\n')
	if idx < 0 {
		return len(text), len(text)
	}
	return start + idx, start + idx + 1
}

// isFenceLine 判断一行是否是围栏代码块的开头（至少三个反引号或波浪号）。
func isFenceLine(line string) bool {
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

// scanFence 从围栏之后的第一行开始找收尾围栏。
// 返回是否找到，以及代码块结束位置（收尾围栏行的行尾；未找到时是文末）。
func scanFence(text string, start int, marker string) (closed bool, end int) {
	pos := start
	for pos < len(text) {
		lineEnd, next := lineBounds(text, pos)
		line := strings.TrimSpace(strings.TrimRight(text[pos:lineEnd], "\r"))
		if strings.HasPrefix(line, marker) {
			return true, pos + len(strings.TrimRight(text[pos:lineEnd], "\r"))
		}
		pos = next
	}
	return false, len(text)
}

// parseHeading 识别 ATX 标题：# 到 ######，且 # 之后必须是空格或行尾。
//
// "#tag" 不是标题——这条规则让正文里以井号开头的标签行不会被误判。
func parseHeading(line string) (level int, title string, ok bool) {
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	rest := line[level:]
	if rest == "" {
		// 只有井号、没有标题文字（例如 "##"）也算标题，标题为空。
		return level, "", true
	}
	if rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	return level, stripClosingHashes(strings.TrimSpace(rest)), true
}

// stripClosingHashes 去掉 ATX 标题的收尾序列（"## 标题 ##"）。
//
// 必须要求 # 前面有空白：否则 "### C#" 会被剪成 "C" —— 那可能是编程语言的名字。
func stripClosingHashes(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == '#' {
		end--
	}
	if end == len(s) {
		return s
	}
	if end == 0 {
		return ""
	}
	if s[end-1] != ' ' && s[end-1] != '\t' {
		return s
	}
	return strings.TrimSpace(s[:end])
}
