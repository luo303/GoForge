package knowledge

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
