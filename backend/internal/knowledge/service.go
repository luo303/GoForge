// 本文件是知识模块的业务层：校验、编排、事务边界。
//
// 它不 import gin，也不拼 HTTP 响应；失败用本包的哨兵错误表示，
// 由 handler 翻译成统一错误码。这样同一段业务逻辑将来被 worker 复用时，
// 不需要跟着改错误处理。
package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/luo303/mindos/backend/internal/platform"
)

// 模块级哨兵错误。handler 用 errors.Is 判断，不要比较错误字符串。
var (
	// ErrEmptyDocument 表示正文去掉空白后为空——空文档没有可切分的内容。
	ErrEmptyDocument = errors.New("document is empty")
	// ErrNotUTF8 表示内容不是合法 UTF-8（例如误传了 PDF 或压缩包）。
	ErrNotUTF8 = errors.New("document is not valid UTF-8 text")
	// ErrTooLarge 表示超过 STORAGE_MAX_UPLOAD_BYTES。
	ErrTooLarge = errors.New("document exceeds the upload size limit")
	// ErrDuplicate 表示同内容的文档已存在（content_hash 唯一索引冲突）。
	ErrDuplicate = errors.New("document content already exists")
	// ErrNotFound 表示文档不存在。
	ErrNotFound = errors.New("document not found")
)

// 分页边界。上限 100 是"防止一次拉全库"的护栏，不是性能优化。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// documentRepository 是 Service 需要的数据访问能力。
//
// 接口定义在**使用方**而不是 Repository 一侧：Go 的接口是隐式实现的，
// *Repository 什么都不用写就满足它；而测试可以塞一个内存实现，
// 不需要数据库。这是本项目"接口按需定义"的常规做法。
type documentRepository interface {
	CreateWithChunks(ctx context.Context, doc *Document, chunks []Chunk) error
	Get(ctx context.Context, id int64) (*Document, error)
	List(ctx context.Context, limit, offset int) ([]Document, error)
	Count(ctx context.Context) (int64, error)
	ListChunks(ctx context.Context, documentID int64, limit, offset int) ([]Chunk, error)
	CountChunks(ctx context.Context, documentID int64) (int64, error)
}

// fileStore 是原始文件的存储能力。
//
// 刻意只留 Save / Remove：本阶段没有"下载原文"与"重索引"的入口，
// 多声明一个 Open 就多一个没人调用的方法与一份测试负担。
type fileStore interface {
	Save(ctx context.Context, data []byte) (platform.SavedFile, error)
	Remove(ctx context.Context, relPath string) error
}

// Service 编排导入流程：校验 → 解析 → 切分 → 落盘 → 落库。
type Service struct {
	repo           documentRepository
	store          fileStore
	maxUploadBytes int64
}

// NewService 组装业务层。maxUploadBytes 来自配置，<=0 时会被兜底成 10 MiB，
// 避免调用方忘了传导致"任何文件都超限"。
func NewService(repo documentRepository, store fileStore, maxUploadBytes int64) *Service {
	if maxUploadBytes <= 0 {
		maxUploadBytes = 10 << 20
	}
	return &Service{repo: repo, store: store, maxUploadBytes: maxUploadBytes}
}

// FileInput 是一次文件导入请求。
type FileInput struct {
	Filename string
	Content  io.Reader
}

// NoteInput 是一次笔记导入请求。
type NoteInput struct {
	Title   string
	Content string
}

// ImportFile 同步导入一个文本文件。
//
// Phase 4 在请求内完成解析与切分（文件上限 10 MiB，同步执行是可接受的）；
// Phase 6 会把它改成"落盘 + 建 job + 202 受理"，切分搬进 worker，
// 届时 Service 的入参形态基本不变，只是返回 job_id。
func (s *Service) ImportFile(ctx context.Context, in FileInput) (*Document, error) {
	mime, ok := MIMEByFilename(in.Filename)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, filepath.Ext(in.Filename))
	}

	data, err := readAllLimited(in.Content, s.maxUploadBytes)
	if err != nil {
		return nil, err
	}
	text, data, err := normalizeText(data)
	if err != nil {
		return nil, err
	}
	chunks, err := buildChunks(mime, text)
	if err != nil {
		return nil, err
	}

	// 先落盘再落库：文件系统不在数据库事务里，顺序反过来会出现
	// "事务已提交但文件写失败"——那才是真正不可修复的坏状态。
	saved, err := s.store.Save(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("save document file: %w", err)
	}

	doc := &Document{
		Title:       titleFromFilename(in.Filename),
		MIME:        mime,
		SizeBytes:   int64(len(data)),
		ContentHash: saved.Hash,
		SourceType:  SourceFile,
		StoragePath: &saved.RelPath,
		Status:      StatusReady,
		ChunkCount:  len(chunks),
	}
	if err := s.repo.CreateWithChunks(ctx, doc, chunks); err != nil {
		// 补偿清理：库里没留下记录，盘上就不该留文件。
		// Reused 表示这份内容属于更早的一条文档，删了会让那条文档失去原文。
		if !saved.Reused {
			// 两个错误一起返回：清理失败不该掩盖真正的失败原因，
			// 但也不能悄悄吞掉（errors.Join 会保留两条链，日志里都看得到）。
			if rmErr := s.store.Remove(ctx, saved.RelPath); rmErr != nil {
				return nil, errors.Join(err, fmt.Errorf("cleanup stored file: %w", rmErr))
			}
		}
		return nil, err
	}
	return doc, nil
}

// ImportNote 导入一条笔记。
//
// 笔记的正文存在 documents.content，同时也切分成分块：检索与引用走同一套逻辑，
// 不需要为笔记单独开一条查询路径。
//
// 笔记一律按 Markdown 解析：用户自己写的东西里出现 "# 小节" 是常见用法，
// 而纯文本文件里以 # 开头的行更可能是日志，所以两者规则不同。
func (s *Service) ImportNote(ctx context.Context, in NoteInput) (*Document, error) {
	if int64(len(in.Content)) > s.maxUploadBytes {
		return nil, ErrTooLarge
	}
	text, data, err := normalizeText([]byte(in.Content))
	if err != nil {
		return nil, err
	}
	chunks, err := buildChunks(MIMEMarkdown, text)
	if err != nil {
		return nil, err
	}

	doc := &Document{
		Title:       titleForNote(in.Title, text),
		MIME:        MIMEMarkdown,
		SizeBytes:   int64(len(data)),
		ContentHash: platform.ContentHash(data),
		SourceType:  SourceNote,
		Content:     &text,
		Status:      StatusReady,
		ChunkCount:  len(chunks),
	}
	if err := s.repo.CreateWithChunks(ctx, doc, chunks); err != nil {
		return nil, err
	}
	return doc, nil
}

// Get 读取文档详情。
func (s *Service) Get(ctx context.Context, id int64) (*Document, error) {
	return s.repo.Get(ctx, id)
}

// List 分页返回文档，同时给出总数供前端翻页。
func (s *Service) List(ctx context.Context, limit, offset int) ([]Document, int64, error) {
	limit, offset = ClampPaging(limit, offset)
	docs, err := s.repo.List(ctx, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	return docs, total, nil
}

// ListChunks 返回某个文档的分块。
//
// 先读文档再读分块（而不是直接查 chunks）：文档不存在与"文档存在但没有分块"
// 是两种不同结果，前者要 404，后者是合法的空列表。
func (s *Service) ListChunks(ctx context.Context, documentID int64, limit, offset int) ([]Chunk, int64, error) {
	if _, err := s.repo.Get(ctx, documentID); err != nil {
		return nil, 0, err
	}
	limit, offset = ClampPaging(limit, offset)
	chunks, err := s.repo.ListChunks(ctx, documentID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.CountChunks(ctx, documentID)
	if err != nil {
		return nil, 0, err
	}
	return chunks, total, nil
}

// utf8BOM 是文件开头的字节序标记。Windows 记事本保存的 UTF-8 文件常带它。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// normalizeText 统一做三件事：去 BOM、校验编码、拒绝空内容。
//
// 返回值里的 data 是**去掉 BOM 之后**的字节：原始文件、内容哈希、正文偏移
// 三者都以它为基准，这样"偏移能切回原文"在重索引时依然成立。
func normalizeText(data []byte) (text string, cleaned []byte, err error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return "", nil, ErrNotUTF8
	}
	text = string(data)
	if strings.TrimSpace(text) == "" {
		return "", nil, ErrEmptyDocument
	}
	return text, data, nil
}

// buildChunks 是所有来源共用的"解析 + 切分"入口。
func buildChunks(mime, text string) ([]Chunk, error) {
	blocks, err := Parse(mime, text)
	if err != nil {
		return nil, err
	}
	chunks := Split(text, blocks, DefaultChunkOptions)
	if len(chunks) == 0 {
		return nil, ErrEmptyDocument
	}
	return chunks, nil
}

// readAllLimited 读取最多 limit 字节，超出返回 ErrTooLarge。
//
// 多读 1 字节是为了区分"恰好等于上限"与"超过上限"：只读 limit 的话，
// 超限文件会被静默截断成合法文档，那是比直接报错更糟的行为。
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// ClampPaging 把分页参数收进合法区间。
//
// 导出是因为 handler 需要回显"实际生效的 limit/offset"——请求 limit=1000 时
// 实际只返回 100 条，响应里还写 1000 会让前端算错总页数。
// 放在 service 而不是 handler：这是业务约束（一次最多看多少条），
// 换一个入口（worker、CLI）时也该生效。
func ClampPaging(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// titleFromFilename 用文件名（去扩展名）做标题。
func titleFromFilename(name string) string {
	base := filepath.Base(name)
	title := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	if title == "" {
		return "未命名文档"
	}
	return truncateRunes(title, 200)
}

// titleForNote 优先用用户填的标题；没填就用正文第一行。
//
// 用第一行而不是"The story begins..."这种默认值：列表里一眼能认出是哪条笔记，
// 比"未命名笔记 3"有用得多。
func titleForNote(title, content string) string {
	if t := strings.TrimSpace(title); t != "" {
		return truncateRunes(t, 200)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line != "" {
			return truncateRunes(line, 60)
		}
	}
	return "未命名笔记"
}

// truncateRunes 按码点截断，避免把中文或 emoji 切成半个字符。
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit])
}
