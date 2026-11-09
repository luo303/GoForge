package knowledge

import (
	"testing"
)

func TestParseMarkdownBlocks(t *testing.T) {
	text := "# 第一章\n" +
		"第一段第一行\n" +
		"第一段第二行\n" +
		"\n" +
		"## 小节 ##\n" +
		"第二段\n" +
		"\n" +
		"```go\n" +
		"func main() {}\n" +
		"```\n" +
		"\n" +
		"#标签不是标题\n"

	blocks, err := Parse(MIMEMarkdown, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []struct {
		kind    BlockKind
		level   int
		heading string
		body    string
	}{
		{BlockHeading, 1, "第一章", "# 第一章"},
		{BlockParagraph, 0, "", "第一段第一行\n第一段第二行"},
		{BlockHeading, 2, "小节", "## 小节 ##"},
		{BlockParagraph, 0, "", "第二段"},
		{BlockCode, 0, "", "```go\nfunc main() {}\n```"},
		{BlockParagraph, 0, "", "#标签不是标题"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(want), blocks)
	}
	for i, w := range want {
		got := blocks[i]
		if got.Kind != w.kind || got.Level != w.level || got.Heading != w.heading {
			t.Errorf("block %d = {kind:%d level:%d heading:%q}, want {kind:%d level:%d heading:%q}",
				i, got.Kind, got.Level, got.Heading, w.kind, w.level, w.heading)
		}
		// 每个块都必须是原文的真实子串，这是偏移可用的前提。
		if body := text[got.Start:got.End]; body != w.body {
			t.Errorf("block %d body = %q, want %q", i, body, w.body)
		}
	}
}

func TestParseCRLFPreservesOffsets(t *testing.T) {
	// Windows 换行：结构判断要忽略 \r，但块内容仍包含它（偏移落在原始字节上）。
	text := "# 标题\r\n\r\n正文\r\n继续\r\n"

	blocks, err := Parse(MIMEMarkdown, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	if got := text[blocks[0].Start:blocks[0].End]; got != "# 标题" {
		t.Fatalf("heading body = %q", got)
	}
	if got := text[blocks[1].Start:blocks[1].End]; got != "正文\r\n继续" {
		t.Fatalf("paragraph body = %q", got)
	}
}

func TestParsePlainTextIgnoresMarkdown(t *testing.T) {
	// 纯文本里的 "# " 只是普通字符；否则日志与草稿会被误切成章节。
	text := "# 不是标题\n```\n也不是代码块\n```\n"

	blocks, err := Parse(MIMEText, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != BlockParagraph {
		t.Fatalf("kind = %d, want paragraph", blocks[0].Kind)
	}
	if got := text[blocks[0].Start:blocks[0].End]; got != "# 不是标题\n```\n也不是代码块\n```" {
		t.Fatalf("body = %q", got)
	}
}

func TestParseUnclosedFenceRunsToEOF(t *testing.T) {
	text := "```\ncode\nmore code\n"
	blocks, err := Parse(MIMEMarkdown, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Kind != BlockCode {
		t.Fatalf("blocks = %+v, want a single code block", blocks)
	}
	if blocks[0].End != len(text) {
		t.Fatalf("unclosed fence end = %d, want %d", blocks[0].End, len(text))
	}
}

func TestParseEmptyDocument(t *testing.T) {
	for _, text := range []string{"", "   \n\n\t\n"} {
		blocks, err := Parse(MIMEMarkdown, text)
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		if len(blocks) != 0 {
			t.Fatalf("Parse(%q) = %+v, want no blocks", text, blocks)
		}
	}
}

func TestParseUnsupportedMIME(t *testing.T) {
	if _, err := Parse("application/pdf", "x"); err != ErrUnsupportedType {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestMIMEByFilename(t *testing.T) {
	cases := map[string]string{
		"notes.md":      MIMEMarkdown,
		"NOTE.MARKDOWN": MIMEMarkdown,
		"日志.txt":        MIMEText,
		"report.TEXT":   MIMEText,
		"slides.pdf":    "",
		"no-extension":  "",
	}
	for name, want := range cases {
		got, ok := MIMEByFilename(name)
		if got != want || ok != (want != "") {
			t.Errorf("MIMEByFilename(%q) = (%q, %v), want (%q, %v)", name, got, ok, want, want != "")
		}
	}
}
