package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// newTestEngine 只挂本模块的路由，不引入全局中间件：
// handler 测试要验证的是"参数与错误码"，request_id / 日志已有单独的测试。
func newTestEngine(t *testing.T, maxUploadBytes int64) (*gin.Engine, *fakeRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc, repo, _ := newTestService(maxUploadBytes)
	h := NewHandler(svc)

	e := gin.New()
	v1 := e.Group("/api/v1")
	v1.POST("/documents", h.CreateFile)
	v1.POST("/documents/note", h.CreateNote)
	v1.GET("/documents", h.List)
	v1.GET("/documents/:id", h.Get)
	v1.GET("/documents/:id/chunks", h.ListChunks)
	v1.DELETE("/documents/:id", h.Delete)
	v1.POST("/documents/:id/reindex", h.Reindex)
	return e, repo
}

// envelope 镜像统一响应结构，用来断言"成功/失败都走信封"。
type envelope struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

func doJSON(t *testing.T, e *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		req = httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an envelope: %v (%s)", err, rec.Body.String())
	}
	return rec, env
}

func doUpload(t *testing.T, e *gin.Engine, filename string, content []byte) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an envelope: %v (%s)", err, rec.Body.String())
	}
	return rec, env
}

func TestCreateFileEndpoint(t *testing.T) {
	e, repo := newTestEngine(t, 1<<20)

	rec, env := doUpload(t, e, "guide.md", []byte("# 标题\n\n正文内容。\n"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if env.Code != "OK" {
		t.Fatalf("envelope code = %q", env.Code)
	}

	var doc documentView
	if err := json.Unmarshal(env.Data, &doc); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if doc.ID == 0 || doc.Title != "guide" || doc.Status != StatusReady {
		t.Fatalf("unexpected document view: %+v", doc)
	}
	if doc.ChunkCount != 1 {
		t.Fatalf("chunk_count = %d, want 1", doc.ChunkCount)
	}
	if _, ok := repo.chunks[doc.ID]; !ok {
		t.Fatal("chunks should be persisted with the document")
	}
}

// 重复导入同一份内容：200 + existed=true，而不是 409 或 201。
//
// 三种状态码的分工：201 新建、200 命中既有、409 保留给"撞了唯一索引却读不回
// 既有记录"这种已无法解释的情况。
func TestCreateDuplicateReturnsExisting(t *testing.T) {
	e, repo := newTestEngine(t, 1<<20)
	content := []byte("# 标题\n\n正文内容。\n")

	first, _ := doUpload(t, e, "guide.md", content)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}

	rec, env := doUpload(t, e, "copy.md", content)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var duplicate struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Existed bool   `json:"existed"`
	}
	if err := json.Unmarshal(env.Data, &duplicate); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if !duplicate.Existed {
		t.Error("existed must be true for a duplicate import")
	}
	if duplicate.Title != "guide" {
		t.Errorf("title = %q, want the existing document's title", duplicate.Title)
	}
	if len(repo.docs) != 1 {
		t.Errorf("documents = %d, want 1", len(repo.docs))
	}

	// 笔记走同一条规则。
	noteBody := map[string]string{"content": "# 会议记录\n\n结论。\n"}
	if rec, _ := doJSON(t, e, http.MethodPost, "/api/v1/documents/note", noteBody); rec.Code != http.StatusCreated {
		t.Fatalf("first note status = %d", rec.Code)
	}
	rec, env = doJSON(t, e, http.MethodPost, "/api/v1/documents/note", noteBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate note status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var duplicateNote struct {
		Existed bool `json:"existed"`
	}
	if err := json.Unmarshal(env.Data, &duplicateNote); err != nil {
		t.Fatalf("decode note: %v", err)
	}
	if !duplicateNote.Existed {
		t.Error("existed must be true for a duplicate note")
	}
}

func TestCreateFileRejectsUnsupportedType(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	rec, env := doUpload(t, e, "slides.pdf", []byte("%PDF-1.4"))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if env.Code != "UNSUPPORTED_MEDIA_TYPE" {
		t.Fatalf("code = %q", env.Code)
	}
}

func TestCreateFileRejectsMissingField(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	// 没有 file 字段的 multipart：必须给出明确提示，而不是 500。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=none")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCreateFileRejectsOversizedUpload(t *testing.T) {
	e, _ := newTestEngine(t, 8)

	rec, env := doUpload(t, e, "big.txt", []byte("123456789"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if env.Code != "PAYLOAD_TOO_LARGE" {
		t.Fatalf("code = %q", env.Code)
	}
}

func TestCreateNoteEndpoint(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	rec, env := doJSON(t, e, http.MethodPost, "/api/v1/documents/note", map[string]string{
		"content": "# 会议纪要\n\n下周交付。\n",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var doc documentView
	if err := json.Unmarshal(env.Data, &doc); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if doc.Title != "会议纪要" || doc.SourceType != SourceNote || doc.MIME != MIMEMarkdown {
		t.Fatalf("unexpected document: %+v", doc)
	}
}

func TestCreateNoteRejectsEmptyContent(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	rec, env := doJSON(t, e, http.MethodPost, "/api/v1/documents/note", map[string]string{
		"content": "   \n\n",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if env.Code != "INVALID_ARGUMENT" {
		t.Fatalf("code = %q", env.Code)
	}
}

func TestCreateNoteRejectsMalformedJSON(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/note", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestListAndDetailEndpoints(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	for _, content := range []string{"第一条笔记。", "第二条笔记。"} {
		if rec, env := doJSON(t, e, http.MethodPost, "/api/v1/documents/note",
			map[string]string{"content": content}); rec.Code != http.StatusCreated {
			t.Fatalf("seed note: status = %d, %s", rec.Code, env.Message)
		}
	}

	rec, env := doJSON(t, e, http.MethodGet, "/api/v1/documents?limit=1&offset=0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var list documentListView
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Total != 2 || len(list.Items) != 1 || list.Limit != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}

	rec, env = doJSON(t, e, http.MethodGet, "/api/v1/documents/1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d", rec.Code)
	}
	var doc documentView
	if err := json.Unmarshal(env.Data, &doc); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if doc.ID != 1 {
		t.Fatalf("detail id = %d", doc.ID)
	}
}

func TestDetailAndChunksErrorBranches(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	// 非整数 id：参数错误，不是 404。
	if rec, env := doJSON(t, e, http.MethodGet, "/api/v1/documents/abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, env.Code)
	}
	// 不存在的 id：404。
	if rec, env := doJSON(t, e, http.MethodGet, "/api/v1/documents/999", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, env.Code)
	}
	// 分块接口对不存在的文档同样 404。
	if rec, env := doJSON(t, e, http.MethodGet, "/api/v1/documents/999/chunks", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, env.Code)
	}
	// 非法分页参数：400，而不是静默当成 0。
	if rec, env := doJSON(t, e, http.MethodGet, "/api/v1/documents?limit=abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, env.Code)
	}
}

func TestListChunksEndpoint(t *testing.T) {
	e, _ := newTestEngine(t, 1<<20)

	doc := map[string]string{"content": "# 标题\n\n第一段。\n\n## 小节\n\n第二段。"}
	_, env := doJSON(t, e, http.MethodPost, "/api/v1/documents/note", doc)
	var created documentView
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("decode document: %v", err)
	}

	rec, env := doJSON(t, e, http.MethodGet,
		"/api/v1/documents/1/chunks?limit=10&offset=0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list chunkListView
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatalf("decode chunks: %v", err)
	}
	if list.Total != int64(created.ChunkCount) || len(list.Items) == 0 {
		t.Fatalf("unexpected chunk list: %+v", list)
	}
	first := list.Items[0]
	if first.Seq != 0 || first.Content == "" || first.EndOffset <= first.StartOffset {
		t.Fatalf("unexpected first chunk: %+v", first)
	}
	if first.TokenCount <= 0 {
		t.Fatalf("token_count should be positive: %+v", first)
	}
}

func TestDeleteEndpoint(t *testing.T) {
	e, repo := newTestEngine(t, 1<<20)
	if rec, _ := doJSON(t, e, http.MethodPost, "/api/v1/documents/note",
		map[string]string{"content": "# 待删除\n\n正文。\n"}); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}

	rec, env := doJSON(t, e, http.MethodDelete, "/api/v1/documents/1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var deleted deletedView
	if err := json.Unmarshal(env.Data, &deleted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if deleted.ID != 1 {
		t.Errorf("deleted id = %d, want 1", deleted.ID)
	}
	if len(repo.docs) != 0 {
		t.Error("document should be gone")
	}

	// 再删一次是 404，而不是"又成功了一次"。
	rec, env = doJSON(t, e, http.MethodDelete, "/api/v1/documents/1", nil)
	if rec.Code != http.StatusNotFound || env.Code != "NOT_FOUND" {
		t.Fatalf("second delete: status = %d code = %q", rec.Code, env.Code)
	}
}

func TestReindexEndpoint(t *testing.T) {
	e, repo := newTestEngine(t, 1<<20)
	if rec, _ := doJSON(t, e, http.MethodPost, "/api/v1/documents/note",
		map[string]string{"content": "# 标题\n\n第一段。\n\n## 小节\n\n第二段。\n"}); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}

	rec, env := doJSON(t, e, http.MethodPost, "/api/v1/documents/1/reindex", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var doc documentView
	if err := json.Unmarshal(env.Data, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.ChunkCount != len(repo.chunks[1]) || doc.ChunkCount == 0 {
		t.Errorf("chunk_count = %d, stored = %d", doc.ChunkCount, len(repo.chunks[1]))
	}
}

// 缺原文重索引是"数据状态坏了"，不是参数错，所以映射成 409 而不是 400/500。
func TestMapErrorSourceMissing(t *testing.T) {
	err := mapError(fmt.Errorf("reindex: %w", ErrSourceMissing))
	if !httpx.IsCode(err, "CONFLICT") {
		t.Fatalf("code = %v, want CONFLICT", err)
	}
}
