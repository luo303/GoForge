// 本文件是知识模块的 HTTP 入口：绑定参数、把哨兵错误翻译成统一错误码。
//
// 它不做任何业务判断——"文件不能为空""内容要 UTF-8"都在 service 里，
// 这样同样的规则在 Phase 6 搬进 worker 时不需要重写一份。
package knowledge

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// Handler 提供 /documents 相关接口。
type Handler struct {
	svc *Service
}

// NewHandler 创建 handler。依赖由 cmd/server 装配，router 只负责挂路由。
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// documentView 是文档对外的 JSON 形状。
//
// 单独定义而不是直接序列化 model：数据库列会随阶段增加（collection、tags、
// error_*），把模型直接暴露给前端等于让表结构变成接口契约，加一列就可能
// 影响前端类型。视图层只挑本次要用的字段。
type documentView struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	SourceType  string    `json:"source_type"`
	MIME        string    `json:"mime"`
	SizeBytes   int64     `json:"size_bytes"`
	ContentHash string    `json:"content_hash"`
	Status      string    `json:"status"`
	ChunkCount  int       `json:"chunk_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type chunkView struct {
	ID          int64  `json:"id"`
	Seq         int    `json:"seq"`
	Content     string `json:"content"`
	Heading     string `json:"heading,omitempty"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
	TokenCount  int    `json:"token_count"`
}

type documentListView struct {
	Items  []documentView `json:"items"`
	Total  int64          `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

type chunkListView struct {
	Items  []chunkView `json:"items"`
	Total  int64       `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

type createNoteRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// CreateFile 处理 multipart 上传：POST /documents，字段名 file。
func (h *Handler) CreateFile(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage(
			`multipart field "file" is required`))
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		httpx.Fail(c, httpx.ErrInternal.Wrap(err))
		return
	}
	// defer 写在 Open 之后立刻执行：中间任何一条 return 都会关掉它，
	// 否则句柄会一直占着临时文件，Windows 下甚至删不掉。
	defer func() { _ = f.Close() }()

	doc, err := h.svc.ImportFile(c.Request.Context(), FileInput{
		Filename: fileHeader.Filename,
		Content:  f,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.Created(c, newDocumentView(doc))
}

// CreateNote 处理笔记导入：POST /documents/note。
func (h *Handler) CreateNote(c *gin.Context) {
	var req createNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage(
			`request body must be JSON: {"title":"...","content":"..."}`))
		return
	}

	doc, err := h.svc.ImportNote(c.Request.Context(), NoteInput{
		Title:   req.Title,
		Content: req.Content,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.Created(c, newDocumentView(doc))
}

// List 返回分页后的文档列表：GET /documents?limit=&offset=。
func (h *Handler) List(c *gin.Context) {
	limit, offset, err := parsePaging(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	docs, total, err := h.svc.List(c.Request.Context(), limit, offset)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}

	items := make([]documentView, 0, len(docs))
	for i := range docs {
		items = append(items, newDocumentView(&docs[i]))
	}
	httpx.OK(c, documentListView{Items: items, Total: total, Limit: limit, Offset: offset})
}

// Get 返回文档详情：GET /documents/:id。
func (h *Handler) Get(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	doc, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, newDocumentView(doc))
}

// ListChunks 返回分块列表：GET /documents/:id/chunks?limit=&offset=。
//
// 分块会按 seq 升序返回；前端用它做"引用跳转"和调试预览。
func (h *Handler) ListChunks(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	limit, offset, err := parsePaging(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	chunks, total, err := h.svc.ListChunks(c.Request.Context(), id, limit, offset)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}

	items := make([]chunkView, 0, len(chunks))
	for i := range chunks {
		items = append(items, newChunkView(&chunks[i]))
	}
	httpx.OK(c, chunkListView{Items: items, Total: total, Limit: limit, Offset: offset})
}

func newDocumentView(doc *Document) documentView {
	return documentView{
		ID:          doc.ID,
		Title:       doc.Title,
		SourceType:  doc.SourceType,
		MIME:        doc.MIME,
		SizeBytes:   doc.SizeBytes,
		ContentHash: doc.ContentHash,
		Status:      doc.Status,
		ChunkCount:  doc.ChunkCount,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}
}

func newChunkView(c *Chunk) chunkView {
	view := chunkView{
		ID:          c.ID,
		Seq:         c.Seq,
		Content:     c.Content,
		StartOffset: c.StartOffset,
		EndOffset:   c.EndOffset,
		TokenCount:  c.TokenCount,
	}
	if c.Heading != nil {
		view.Heading = *c.Heading
	}
	return view
}

// parseID 只接受正整数。strconv 的错误信息是英文且带实现细节，
// 这里统一换成面向调用方的说法。
func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.ErrInvalidArgument.WithMessage(
			"document id must be a positive integer, got %q", raw)
	}
	return id, nil
}

// parsePaging 解析 limit/offset，缺省交给 ClampPaging 补默认值。
func parsePaging(c *gin.Context) (limit, offset int, err error) {
	limit, err = parseIntQuery(c, "limit")
	if err != nil {
		return 0, 0, err
	}
	offset, err = parseIntQuery(c, "offset")
	if err != nil {
		return 0, 0, err
	}
	limit, offset = ClampPaging(limit, offset)
	return limit, offset, nil
}

func parseIntQuery(c *gin.Context, key string) (int, error) {
	raw := c.Query(key)
	if raw == "" {
		return 0, nil
	}
	v, convErr := strconv.Atoi(raw)
	if convErr != nil {
		return 0, httpx.ErrInvalidArgument.WithMessage(
			"%s must be an integer, got %q", key, raw)
	}
	return v, nil
}

// mapError 把模块哨兵错误翻译成对外错误码。
//
// 默认分支原样返回：不是本模块认识的错误，说明是数据库/IO 故障，
// 交给 httpx.Fail 兜底成 500，原始原因只进日志不进响应。
func mapError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.ErrNotFound.WithMessage("document not found")
	case errors.Is(err, ErrDuplicate):
		return httpx.ErrConflict.WithMessage("a document with identical content already exists")
	case errors.Is(err, ErrUnsupportedType):
		return httpx.ErrUnsupportedMediaType.WithMessage(
			"only Markdown (.md, .markdown) and plain text (.txt) files are supported")
	case errors.Is(err, ErrTooLarge):
		return httpx.ErrPayloadTooLarge.WithMessage("document exceeds the configured upload limit")
	case errors.Is(err, ErrEmptyDocument):
		return httpx.ErrInvalidArgument.WithMessage("document content must not be empty")
	case errors.Is(err, ErrNotUTF8):
		return httpx.ErrInvalidArgument.WithMessage("document must be UTF-8 encoded text")
	default:
		return err
	}
}
