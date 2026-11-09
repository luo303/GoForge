package knowledge

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/platform"
)

// fakeRepo 是内存版仓储。Service 依赖的是接口，所以这里不需要数据库。
//
// 价值在于能精确制造"写库失败"，而真实数据库很难在"文档已插入、分块插入失败"
// 这个中间点稳定地报错。
type fakeRepo struct {
	docs      map[int64]*Document
	chunks    map[int64][]Chunk
	hashIndex map[string]int64
	nextID    int64
	createErr error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		docs:      map[int64]*Document{},
		chunks:    map[int64][]Chunk{},
		hashIndex: map[string]int64{},
	}
}

func (f *fakeRepo) CreateWithChunks(_ context.Context, doc *Document, chunks []Chunk) error {
	if f.createErr != nil {
		return f.createErr
	}
	// 复刻 documents_content_hash_key 唯一索引：真实数据库靠它兜底幂等，
	// 假仓储如果不模拟，service 的"冲突后读回"分支就永远不会被走到。
	if _, dup := f.hashIndex[doc.ContentHash]; dup {
		return ErrDuplicate
	}
	f.nextID++
	doc.ID = f.nextID
	// 与真实 Repository 保持一致：chunk_count 由写入方按实际分块数赋值。
	doc.ChunkCount = len(chunks)
	doc.CreatedAt = time.Now().UTC()
	doc.UpdatedAt = doc.CreatedAt
	stored := *doc
	f.docs[doc.ID] = &stored
	f.hashIndex[doc.ContentHash] = doc.ID
	for i := range chunks {
		chunks[i].DocumentID = doc.ID
	}
	f.chunks[doc.ID] = append([]Chunk(nil), chunks...)
	return nil
}

func (f *fakeRepo) GetByContentHash(_ context.Context, hash string) (*Document, error) {
	id, ok := f.hashIndex[hash]
	if !ok {
		return nil, ErrNotFound
	}
	return f.Get(context.Background(), id)
}

func (f *fakeRepo) Get(_ context.Context, id int64) (*Document, error) {
	doc, ok := f.docs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return doc, nil
}

func (f *fakeRepo) List(_ context.Context, limit, offset int) ([]Document, error) {
	var out []Document
	for id := int64(1); id <= f.nextID; id++ {
		if doc, ok := f.docs[id]; ok {
			out = append(out, *doc)
		}
	}
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) Count(_ context.Context) (int64, error) {
	return int64(len(f.docs)), nil
}

func (f *fakeRepo) ListChunks(_ context.Context, documentID int64, limit, offset int) ([]Chunk, error) {
	out := f.chunks[documentID]
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) CountChunks(_ context.Context, documentID int64) (int64, error) {
	return int64(len(f.chunks[documentID])), nil
}

// fakeStore 记录落盘与删除，用于验证失败补偿。
type fakeStore struct {
	files   map[string][]byte
	reused  bool
	saveErr error
	removed []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{files: map[string][]byte{}}
}

func (f *fakeStore) Save(_ context.Context, data []byte) (platform.SavedFile, error) {
	if f.saveErr != nil {
		return platform.SavedFile{}, f.saveErr
	}
	hash := platform.ContentHash(data)
	path := "documents/" + hash
	if _, ok := f.files[path]; !ok {
		f.files[path] = append([]byte(nil), data...)
		return platform.SavedFile{RelPath: path, Hash: hash, Size: int64(len(data))}, nil
	}
	return platform.SavedFile{RelPath: path, Hash: hash, Size: int64(len(data)), Reused: true}, nil
}

func (f *fakeStore) Remove(_ context.Context, relPath string) error {
	f.removed = append(f.removed, relPath)
	delete(f.files, relPath)
	return nil
}

func newTestService(maxUploadBytes int64) (*Service, *fakeRepo, *fakeStore) {
	repo, store := newFakeRepo(), newFakeStore()
	return NewService(repo, store, maxUploadBytes), repo, store
}

func TestImportFileMarkdown(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	text := "# 标题\n\n第一段内容。\n\n## 小节\n\n第二段内容。\n"

	doc, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "guide.md",
		Content:  strings.NewReader(text),
	})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}

	if doc.Title != "guide" {
		t.Errorf("Title = %q, want guide", doc.Title)
	}
	if doc.SourceType != SourceFile || doc.MIME != MIMEMarkdown {
		t.Errorf("unexpected type/mime: %s/%s", doc.SourceType, doc.MIME)
	}
	if doc.Status != StatusReady {
		t.Errorf("Status = %q, want ready", doc.Status)
	}
	if doc.SizeBytes != int64(len(text)) {
		t.Errorf("SizeBytes = %d, want %d", doc.SizeBytes, len(text))
	}
	if doc.StoragePath == nil || *doc.StoragePath == "" {
		t.Fatal("file import must record storage_path")
	}
	if doc.Content != nil {
		t.Error("file import should not copy body into documents.content")
	}
	if doc.ContentHash != platform.ContentHash([]byte(text)) {
		t.Error("content hash must be SHA-256 of the raw bytes")
	}

	chunks := repo.chunks[doc.ID]
	if len(chunks) != doc.ChunkCount || len(chunks) == 0 {
		t.Fatalf("chunk_count = %d, stored chunks = %d", doc.ChunkCount, len(chunks))
	}
	runes := []rune(text)
	for i, c := range chunks {
		if c.DocumentID != doc.ID {
			t.Errorf("chunk %d document_id = %d", i, c.DocumentID)
		}
		if c.Seq != i {
			t.Errorf("chunk %d seq = %d", i, c.Seq)
		}
		if got := string(runes[c.StartOffset:c.EndOffset]); got != c.Content {
			t.Errorf("chunk %d offsets do not map back to text", i)
		}
	}
	// 第二段应挂在"小节"这个标题下。
	last := chunks[len(chunks)-1]
	if last.Heading == nil || *last.Heading != "小节" {
		t.Errorf("last chunk heading = %v, want 小节", last.Heading)
	}
	if len(store.files) != 1 {
		t.Errorf("expected exactly one stored file, got %d", len(store.files))
	}
}

func TestImportFileRejectsUnsupportedType(t *testing.T) {
	svc, _, store := newTestService(1 << 20)

	_, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "slides.pdf",
		Content:  strings.NewReader("%PDF-1.4"),
	})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
	if len(store.files) != 0 {
		t.Fatal("unsupported file must not be stored")
	}
}

func TestImportFileRejectsNonUTF8AndEmpty(t *testing.T) {
	svc, _, store := newTestService(1 << 20)
	ctx := context.Background()

	if _, _, err := svc.ImportFile(ctx, FileInput{
		Filename: "binary.md",
		Content:  bytes.NewReader([]byte{0xff, 0xfe, 0x00}),
	}); !errors.Is(err, ErrNotUTF8) {
		t.Fatalf("non-UTF8 err = %v, want ErrNotUTF8", err)
	}

	if _, _, err := svc.ImportFile(ctx, FileInput{
		Filename: "empty.txt",
		Content:  strings.NewReader(" \n\n\t\n"),
	}); !errors.Is(err, ErrEmptyDocument) {
		t.Fatalf("empty err = %v, want ErrEmptyDocument", err)
	}

	if len(store.files) != 0 {
		t.Fatal("invalid documents must not be stored")
	}
}

func TestImportFileRejectsOversizedFile(t *testing.T) {
	svc, _, store := newTestService(8)

	_, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "big.txt",
		Content:  strings.NewReader("123456789"),
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if len(store.files) != 0 {
		t.Fatal("oversized file must not be stored")
	}

	// 恰好等于上限应当通过：读 limit+1 字节再判断，不能把边界值误判成超限。
	if _, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "exact.txt",
		Content:  strings.NewReader("12345678"),
	}); err != nil {
		t.Fatalf("exact-limit file should be accepted: %v", err)
	}
}

func TestImportFileRemovesFileWhenPersistFails(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	repo.createErr = errors.New("insert chunks failed")

	_, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "note.md",
		Content:  strings.NewReader("# 标题\n\n正文。\n"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(store.removed) != 1 {
		t.Fatalf("stored file should be cleaned up, removed = %v", store.removed)
	}
	if len(store.files) != 0 {
		t.Fatal("storage should be empty after rollback")
	}
}

// 第二次导入相同内容时文件已在盘上（Reused=true），写入会撞唯一索引。
//
// Phase 4 这里断言的是"把 409 抛出去、但不删文件"；Phase 5 起撞索引等于
// 幂等命中，所以正确结果是返回既有文档。文件仍然不能删——它属于第一条文档。
func TestImportReusedFileBecomesIdempotentHit(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	content := "# 标题\n\n正文。\n"

	first, existed, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "first.md",
		Content:  strings.NewReader(content),
	})
	if err != nil || existed {
		t.Fatalf("first import: existed=%v err=%v", existed, err)
	}

	second, existed, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "second.md",
		Content:  strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !existed {
		t.Error("second import must be an idempotent hit")
	}
	if second.ID != first.ID {
		t.Errorf("second import id = %d, want %d", second.ID, first.ID)
	}
	if len(repo.docs) != 1 {
		t.Errorf("documents = %d, want 1", len(repo.docs))
	}
	if len(store.removed) != 0 {
		t.Fatalf("reused file belongs to the earlier document and must not be removed: %v", store.removed)
	}
	if len(store.files) != 1 {
		t.Fatalf("stored files = %d, want 1", len(store.files))
	}
}

func TestImportNoteDerivesTitleAndStoresContent(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	content := "# 会议纪要\n\n下周交付。\n"

	doc, _, err := svc.ImportNote(context.Background(), NoteInput{Content: content})
	if err != nil {
		t.Fatalf("ImportNote: %v", err)
	}
	if doc.Title != "会议纪要" {
		t.Errorf("Title = %q, want 会议纪要", doc.Title)
	}
	if doc.Content == nil || *doc.Content != content {
		t.Error("note content must be stored in documents.content")
	}
	if doc.StoragePath != nil {
		t.Error("note should not create a file on disk")
	}
	if len(store.files) != 0 {
		t.Error("note must not touch the file store")
	}
	if len(repo.chunks[doc.ID]) == 0 {
		t.Error("note must also be chunked")
	}

	// 显式标题优先于正文首行。
	doc2, _, err := svc.ImportNote(context.Background(), NoteInput{
		Title:   "自定义标题",
		Content: "完全不同的正文。",
	})
	if err != nil {
		t.Fatalf("ImportNote with title: %v", err)
	}
	if doc2.Title != "自定义标题" {
		t.Errorf("Title = %q, want 自定义标题", doc2.Title)
	}
}

func TestImportNoteRejectsEmptyAndOversized(t *testing.T) {
	svc, _, _ := newTestService(4)
	ctx := context.Background()

	if _, _, err := svc.ImportNote(ctx, NoteInput{Content: "   \n"}); !errors.Is(err, ErrEmptyDocument) {
		t.Fatalf("empty note err = %v, want ErrEmptyDocument", err)
	}
	if _, _, err := svc.ImportNote(ctx, NoteInput{Content: "五个字符"}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized note err = %v, want ErrTooLarge", err)
	}
}

func TestImportStripsBOMAndKeepsOffsets(t *testing.T) {
	svc, repo, _ := newTestService(1 << 20)
	content := "\ufeff# 标题\n\n正文。\n"

	doc, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "bom.md",
		Content:  strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}

	chunks := repo.chunks[doc.ID]
	if strings.HasPrefix(chunks[0].Content, "\ufeff") {
		t.Fatal("BOM must not leak into chunk content")
	}
	// 第一个分块从 0 开始，指向去掉 BOM 后的文本；
	// 若 BOM 被当作正文，偏移会整体后移一个码点。
	if chunks[0].StartOffset != 0 {
		t.Fatalf("first chunk start = %d, want 0", chunks[0].StartOffset)
	}
	cleaned := []rune(strings.TrimPrefix(content, "\ufeff"))
	if got := string(cleaned[chunks[0].StartOffset:chunks[0].EndOffset]); got != chunks[0].Content {
		t.Fatalf("offsets do not map back after BOM strip")
	}
}

func TestGetMissingDocument(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	if _, err := svc.Get(context.Background(), 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListClampsPaging(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := svc.ImportNote(ctx, NoteInput{Content: string(rune('a'+i)) + " 号笔记"}); err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}

	docs, total, err := svc.List(ctx, 0, -5)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(docs) != 3 {
		t.Fatalf("total = %d, len = %d, want 3/3", total, len(docs))
	}

	docs, _, err = svc.List(ctx, 2, 1)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("limit 2 returned %d docs", len(docs))
	}
}

func TestListChunksMissingDocument(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	if _, _, err := svc.ListChunks(context.Background(), 7, 10, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestClampPaging(t *testing.T) {
	cases := []struct{ limit, offset, wantLimit, wantOffset int }{
		{0, 0, defaultPageSize, 0},
		{-1, -1, defaultPageSize, 0},
		{maxPageSize + 1, 0, maxPageSize, 0},
		{5, 10, 5, 10},
	}
	for _, c := range cases {
		gotLimit, gotOffset := ClampPaging(c.limit, c.offset)
		if gotLimit != c.wantLimit || gotOffset != c.wantOffset {
			t.Errorf("ClampPaging(%d,%d) = (%d,%d), want (%d,%d)",
				c.limit, c.offset, gotLimit, gotOffset, c.wantLimit, c.wantOffset)
		}
	}
}

// 幂等导入：同样的内容再传一次，返回既有文档，不建第二条记录、不产生第二份文件。
func TestImportFileIsIdempotent(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	text := "# 标题\n\n同一份内容。\n"

	first, existed, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader(text),
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if existed {
		t.Error("first import must not be reported as existing")
	}
	firstChunks := len(repo.chunks[first.ID])

	// 第二次用不同的文件名，但内容一致：标题不能因此被改写。
	second, existed, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "b.md",
		Content:  strings.NewReader(text),
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !existed {
		t.Error("second import must report existed=true")
	}
	if second.ID != first.ID {
		t.Errorf("second import returned id %d, want existing id %d", second.ID, first.ID)
	}
	if second.Title != first.Title {
		t.Errorf("existing document title changed to %q", second.Title)
	}
	if len(repo.docs) != 1 {
		t.Errorf("documents = %d, want 1", len(repo.docs))
	}
	if got := len(repo.chunks[first.ID]); got != firstChunks {
		t.Errorf("chunks = %d, want %d (duplicate must not re-split)", got, firstChunks)
	}
	if len(store.files) != 1 {
		t.Errorf("stored files = %d, want 1", len(store.files))
	}
	// 最要紧的一条：既有文档的原文不能被"补偿清理"删掉。
	if len(store.removed) != 0 {
		t.Errorf("duplicate import removed files: %v", store.removed)
	}
}

// 库里已有记录、但盘上文件丢了（例如手工删了 storage 目录）：
// 这次 Save 会真的写一份新文件，随后的冲突分支仍然不能删它——
// 那条既有文档此刻正指着这个路径。
func TestImportFileDuplicateKeepsRewrittenFile(t *testing.T) {
	svc, _, store := newTestService(1 << 20)
	text := "# 标题\n\n原文。\n"

	first, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader(text),
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	store.files = map[string][]byte{} // 模拟原文丢失

	if _, existed, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader(text),
	}); err != nil || !existed {
		t.Fatalf("second import: existed=%v err=%v", existed, err)
	}

	if len(store.removed) != 0 {
		t.Errorf("duplicate import removed the rewritten file: %v", store.removed)
	}
	if _, ok := store.files[*first.StoragePath]; !ok {
		t.Error("rewritten file must stay on disk for the existing document")
	}
}

// 笔记与文件共用同一条幂等规则：内容相同就是同一条记录。
func TestImportNoteIsIdempotent(t *testing.T) {
	svc, repo, _ := newTestService(1 << 20)
	content := "# 会议记录\n\n结论：先做幂等。\n"

	first, existed, err := svc.ImportNote(context.Background(), NoteInput{Content: content})
	if err != nil || existed {
		t.Fatalf("first note: existed=%v err=%v", existed, err)
	}

	second, existed, err := svc.ImportNote(context.Background(), NoteInput{
		Title:   "另一个标题",
		Content: content,
	})
	if err != nil {
		t.Fatalf("second note: %v", err)
	}
	if !existed || second.ID != first.ID {
		t.Errorf("second note = id %d existed=%v, want id %d existed=true", second.ID, existed, first.ID)
	}
	if len(repo.docs) != 1 {
		t.Errorf("documents = %d, want 1", len(repo.docs))
	}
}
