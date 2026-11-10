// 本文件是知识模块的 HTTP 入口：绑定参数、把哨兵错误翻译成统一错误码。
//
// 它不做任何业务判断——"文件不能为空""内容要 UTF-8"都在 service 里，
// 这样同样的规则在 Phase 6 搬进 worker 时不需要重写一份。
package knowledge

import (
	"errors"
	"strconv"
	"strings"
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
	Collection  string    `json:"collection"`
	Tags        []string  `json:"tags"`
	Status      string    `json:"status"`
	ChunkCount  int       `json:"chunk_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// importedDocumentView 是导入接口的响应：文档视图 + 本次是否命中既有记录。
//
// 用内嵌而不是给 documentView 加字段：existed 只对"导入"这一个动作有意义，
// 挂在详情/列表的视图上会让前端以为它是文档的固有属性。
// 内嵌结构体的字段在 JSON 里会被提升到同一层，因此响应形状仍是扁平的。
// acceptedDocumentView 是"已受理、结果稍后产生"这类响应的公共形状。
//
// 导入与重索引都会返回它：两者都只是把活排进队列，响应里能给的只有
// 当前文档快照与任务 id。抽象出来是为了让两个接口的响应形状不至于漂移。
type acceptedDocumentView struct {
	documentView
	JobID int64 `json:"job_id"`
}

type importedDocumentView struct {
	acceptedDocumentView
	Existed bool `json:"existed"`
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

// deletedView 只回一个 id。删掉的东西没法再回详情，
// 回 id 是为了让调用方确认"删的确实是这一条"，而不是靠状态码猜。
type deletedView struct {
	ID int64 `json:"id"`
}

type chunkListView struct {
	Items  []chunkView `json:"items"`
	Total  int64       `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

type createNoteRequest struct {
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Collection string   `json:"collection"`
	Tags       []string `json:"tags"`
}

// updateDocumentRequest 是 PATCH 的入参。
//
// 三个字段都是指针：JSON 里没出现的键留 nil（不改），出现但为空串/空数组
// 表示"清空"。用值类型就没法表达"没传 tags"，只能靠额外的字段名列表。
type updateDocumentRequest struct {
	Title      *string   `json:"title"`
	Collection *string   `json:"collection"`
	Tags       *[]string `json:"tags"`
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

	res, err := h.svc.StartFileImport(c.Request.Context(), FileInput{
		Filename:   fileHeader.Filename,
		Content:    f,
		Collection: c.PostForm("collection"),
		// 表单里的标签是逗号分隔的一串（`tags=工作,Go`）：
		// multipart 没有数组语义，前端传多值字段反而要自己拼 index。
		Tags: splitCSV(c.PostForm("tags")),
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	respondImported(c, res)
}

// CreateNote 处理笔记导入：POST /documents/note。
func (h *Handler) CreateNote(c *gin.Context) {
	var req createNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage(
			`request body must be JSON: {"title":"...","content":"..."}`))
		return
	}

	res, err := h.svc.StartNoteImport(c.Request.Context(), NoteInput{
		Title:      req.Title,
		Content:    req.Content,
		Collection: req.Collection,
		Tags:       req.Tags,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	respondImported(c, res)
}

// List 返回分页后的文档列表：GET /documents?limit=&offset=&collection=&tag=&status=。
func (h *Handler) List(c *gin.Context) {
	limit, offset, err := parsePaging(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	filter, err := parseFilter(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	docs, total, err := h.svc.List(c.Request.Context(), filter, limit, offset)
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

// Update 局部更新文档元信息：PATCH /documents/:id。
func (h *Handler) Update(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req updateDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.ErrInvalidArgument.WithMessage(
			`request body must be JSON with any of "title", "collection", "tags"`))
		return
	}

	doc, err := h.svc.Update(c.Request.Context(), id, UpdateInput{
		Title:      req.Title,
		Collection: req.Collection,
		Tags:       req.Tags,
	})
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, newDocumentView(doc))
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

// Delete 删除文档：DELETE /documents/:id。
//
// 用 200 + {"id":n} 而不是 204：本项目所有响应都走统一信封，
// 204 按规范不能带 body，会让客户端多写一条"这个接口没有信封"的例外。
func (h *Handler) Delete(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.OK(c, deletedView{ID: id})
}

// Reindex 重新切分文档：POST /documents/:id/reindex。
func (h *Handler) Reindex(c *gin.Context) {
	id, err := parseID(c.Param("id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.StartReindex(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, mapError(err))
		return
	}
	httpx.Accepted(c, acceptedDocumentView{
		documentView: newDocumentView(res.Document),
		JobID:        res.JobID,
	})
}

// respondImported 按"是否新建"选择状态码。
//
// 202 表示"已受理、任务排队中"：文档此时是 pending，分块要等 worker 跑完。
// 200 表示命中既有文档（幂等命中）：没有新建任务，也没有任何处理在进行。
//
// 为什么不再用 201：201 的语义是"资源创建完成"，而异步导入只完成了
// "记录与任务落库"这一步。用 201 会让调用方以为可以立刻读分块。
// 区分 200/202 仍然有意义——只凭状态码就知道"要不要等"。
func respondImported(c *gin.Context, res *ImportResult) {
	view := importedDocumentView{
		acceptedDocumentView: acceptedDocumentView{
			documentView: newDocumentView(res.Document),
			JobID:        res.JobID,
		},
		Existed: res.Existed,
	}
	if res.Existed {
		httpx.OK(c, view)
		return
	}
	httpx.Accepted(c, view)
}

func newDocumentView(doc *Document) documentView {
	tags := []string(doc.Tags)
	if tags == nil {
		// 空切片而不是 null：前端少写一次 `tags ?? []`，
		// 也让"没有标签"在 JSON 里只有一种形状。
		tags = []string{}
	}
	view := documentView{
		ID:          doc.ID,
		Title:       doc.Title,
		SourceType:  doc.SourceType,
		MIME:        doc.MIME,
		SizeBytes:   doc.SizeBytes,
		ContentHash: doc.ContentHash,
		Tags:        tags,
		Status:      doc.Status,
		ChunkCount:  doc.ChunkCount,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}
	if doc.Collection != nil {
		view.Collection = *doc.Collection
	}
	return view
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

// parseFilter 解析列表筛选条件。空参数一律当作"不过滤"，
// 而不是"筛出空串的那一类"——前端清空下拉框时不会专门删掉这个键。
func parseFilter(c *gin.Context) (DocumentFilter, error) {
	var filter DocumentFilter
	if v := strings.TrimSpace(c.Query("collection")); v != "" {
		filter.Collection = &v
	}
	if v := strings.TrimSpace(c.Query("tag")); v != "" {
		filter.Tag = &v
	}
	if v := strings.TrimSpace(c.Query("status")); v != "" {
		if !isKnownStatus(v) {
			return DocumentFilter{}, httpx.ErrInvalidArgument.WithMessage(
				"status must be one of pending, parsing, indexing, ready, failed, cancelling; got %q", v)
		}
		filter.Status = &v
	}
	return filter, nil
}

// isKnownStatus 与 documents.status 的 CHECK 约束保持一致。
//
// 在 handler 拦一道而不是全靠数据库：非法状态会让查询返回空列表，
// 用户看到的是"筛选后什么都没有"，而不是"你传错了参数"。
func isKnownStatus(status string) bool {
	switch status {
	case StatusPending, StatusParsing, StatusIndexing, StatusReady, StatusFailed, StatusCancelling:
		return true
	default:
		return false
	}
}

// splitCSV 把 `tags=a, b` 拆成 []string。空项交给 service 的 normalizeTags 丢弃，
// 这里不做过滤：拆分与校验是两件事，混在一起以后不好单独测。
func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
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
	case errors.Is(err, ErrSourceMissing):
		return httpx.ErrConflict.WithMessage(
			"original content of this document is missing, reindex is not possible")
	case errors.Is(err, ErrReindexRunning):
		// 不是"参数错"，是"同一动作已经在进行中"——409 比 400 更准确。
		return httpx.ErrConflict.WithMessage("reindex for this document is already running")
	case errors.Is(err, ErrUnsupportedType):
		return httpx.ErrUnsupportedMediaType.WithMessage(
			"only Markdown (.md, .markdown) and plain text (.txt) files are supported")
	case errors.Is(err, ErrTooLarge):
		return httpx.ErrPayloadTooLarge.WithMessage("document exceeds the configured upload limit")
	case errors.Is(err, ErrEmptyDocument):
		return httpx.ErrInvalidArgument.WithMessage("document content must not be empty")
	case errors.Is(err, ErrNotUTF8):
		return httpx.ErrInvalidArgument.WithMessage("document must be UTF-8 encoded text")
	case errors.Is(err, ErrInvalidMetadata):
		// 原始 err 里带着具体是哪个字段、哪个值不合法，这里只保留那份说明。
		return httpx.ErrInvalidArgument.WithMessage("%s", err.Error())
	default:
		return err
	}
}
