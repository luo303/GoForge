package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/job"
	"github.com/luo303/mindos/backend/internal/platform"
)

// fakeRepo 是内存版仓储。Service 依赖的是接口，所以这里不需要数据库。
//
// 价值在于能精确制造"写库失败"，而真实数据库很难在"文档已插入、分块插入失败"
// 这个中间点稳定地报错。
type fakeRepo struct {
	docs       map[int64]*Document
	chunks     map[int64][]Chunk
	hashIndex  map[string]int64
	nextID     int64
	createErr  error
	replaceErr error
	// jobs 记录本次测试里创建过的任务，供"受理后是否入队"这类断言使用。
	jobs []*job.Job
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

// CreateWithJob 复刻"同一事务里写待处理文档与任务"。
// 先走 CreateWithChunks（零分块），保证唯一索引冲突的行为与真实仓储一致。
func (f *fakeRepo) CreateWithJob(_ context.Context, doc *Document, j *job.Job) error {
	if err := f.CreateWithChunks(context.Background(), doc, nil); err != nil {
		return err
	}
	if j != nil {
		j.ID = int64(len(f.jobs) + 1)
		if j.Payload == nil {
			j.Payload = job.Payload{}
		}
		j.Payload["document_id"] = doc.ID
		f.jobs = append(f.jobs, j)
	}
	return nil
}

// FinishImport 复刻"替换分块 + 置 ready"，两件事在真实实现里是同一事务。
func (f *fakeRepo) FinishImport(_ context.Context, documentID int64, chunks []Chunk) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	doc, ok := f.docs[documentID]
	if !ok {
		return ErrNotFound
	}
	for i := range chunks {
		chunks[i].DocumentID = documentID
	}
	f.chunks[documentID] = append([]Chunk(nil), chunks...)
	doc.ChunkCount = len(chunks)
	doc.Status = StatusReady
	doc.UpdatedAt = time.Now().UTC()
	// 处理完成也意味着该文档身上的任务结束了：真实实现里这一步由 worker
	// 写回 succeeded，假仓储没有 worker，就在这里收尾，
	// 否则"处理完之后还能不能再排一条"的行为会与真实实现不一致。
	for _, j := range f.jobs {
		if !j.Status.Active() {
			continue
		}
		if id, ok := j.Payload["document_id"].(int64); ok && id == documentID {
			j.Status = job.StatusSucceeded
		}
	}
	return nil
}

// MarkIndexingWithJob 复刻"置 indexing + 建任务"与部分唯一索引的去重行为：
// 真实库靠 (kind, dedupe_key) where status in (queued,running) 挡住重复排队，
// 假仓储不模拟的话，ErrReindexRunning 分支永远不会被走到。
func (f *fakeRepo) MarkIndexingWithJob(_ context.Context, documentID int64, j *job.Job) error {
	doc, ok := f.docs[documentID]
	if !ok {
		return ErrNotFound
	}
	for _, existing := range f.jobs {
		if existing.Kind != KindDocumentReindex || !existing.Status.Active() {
			continue
		}
		if existing.DedupeKey != nil && j.DedupeKey != nil && *existing.DedupeKey == *j.DedupeKey {
			return ErrDuplicate
		}
	}
	doc.Status = StatusIndexing
	j.ID = int64(len(f.jobs) + 1)
	if j.Payload == nil {
		j.Payload = job.Payload{}
	}
	j.Payload["document_id"] = documentID
	f.jobs = append(f.jobs, j)
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

func (f *fakeRepo) List(_ context.Context, filter DocumentFilter, limit, offset int) ([]Document, error) {
	var out []Document
	for id := int64(1); id <= f.nextID; id++ {
		if doc, ok := f.docs[id]; ok && documentMatches(doc, filter) {
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

func (f *fakeRepo) Count(_ context.Context, filter DocumentFilter) (int64, error) {
	var total int64
	for id := int64(1); id <= f.nextID; id++ {
		if doc, ok := f.docs[id]; ok && documentMatches(doc, filter) {
			total++
		}
	}
	return total, nil
}

// documentMatches 复刻仓储的 WHERE 条件，让筛选在单测里也能验收。
func documentMatches(doc *Document, filter DocumentFilter) bool {
	if filter.Collection != nil {
		if doc.Collection == nil || *doc.Collection != *filter.Collection {
			return false
		}
	}
	if filter.Tag != nil {
		found := false
		for _, tag := range doc.Tags {
			if tag == *filter.Tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Status != nil && doc.Status != *filter.Status {
		return false
	}
	return true
}

func (f *fakeRepo) Update(_ context.Context, id int64, patch DocumentPatch) error {
	doc, ok := f.docs[id]
	if !ok {
		return ErrNotFound
	}
	if patch.Title != nil {
		doc.Title = *patch.Title
	}
	if patch.Collection != nil {
		if *patch.Collection == "" {
			doc.Collection = nil
		} else {
			collection := *patch.Collection
			doc.Collection = &collection
		}
	}
	if patch.Tags != nil {
		doc.Tags = *patch.Tags
	}
	doc.UpdatedAt = time.Now().UTC()
	return nil
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

func (f *fakeRepo) Delete(_ context.Context, id int64) error {
	doc, ok := f.docs[id]
	if !ok {
		return ErrNotFound
	}
	// 真实数据库靠 ON DELETE CASCADE 删分块，假仓储显式做同样的事，
	// 否则"删除后不该留下分块"这条断言在单测里就失去意义。
	delete(f.chunks, id)
	delete(f.hashIndex, doc.ContentHash)
	delete(f.docs, id)
	return nil
}

func (f *fakeRepo) ReplaceChunks(_ context.Context, documentID int64, chunks []Chunk) error {
	doc, ok := f.docs[documentID]
	if !ok {
		return ErrNotFound
	}
	if f.replaceErr != nil {
		// 真实仓储里这一步是单事务：失败时旧分块必须原封不动。
		return f.replaceErr
	}
	for i := range chunks {
		chunks[i].DocumentID = documentID
	}
	f.chunks[documentID] = append([]Chunk(nil), chunks...)
	doc.ChunkCount = len(chunks)
	doc.UpdatedAt = time.Now().UTC()
	return nil
}

// fakeStore 记录落盘与删除，用于验证失败补偿。
type fakeStore struct {
	files     map[string][]byte
	reused    bool
	saveErr   error
	removeErr error
	removed   []string
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

func (f *fakeStore) Read(_ context.Context, relPath string) ([]byte, error) {
	data, ok := f.files[relPath]
	if !ok {
		return nil, fmt.Errorf("read %s: %w", relPath, fs.ErrNotExist)
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeStore) Remove(_ context.Context, relPath string) error {
	f.removed = append(f.removed, relPath)
	if f.removeErr != nil {
		return f.removeErr
	}
	delete(f.files, relPath)
	return nil
}

func newTestService(maxUploadBytes int64) (*Service, *fakeRepo, *fakeStore) {
	repo, store := newFakeRepo(), newFakeStore()
	return NewService(repo, store, maxUploadBytes, nil), repo, store
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

// 分类与标签在入库前被规整：去空白、去重、丢空项，保持输入顺序。
func TestImportStoresCollectionAndTags(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	doc, _, err := svc.ImportNote(context.Background(), NoteInput{
		Content:    "# 带标签\n\n正文。\n",
		Collection: "  工作  ",
		Tags:       []string{" go ", "go", "", "检索"},
	})
	if err != nil {
		t.Fatalf("ImportNote: %v", err)
	}
	if doc.Collection == nil || *doc.Collection != "工作" {
		t.Fatalf("collection = %v, want 工作", doc.Collection)
	}
	if len(doc.Tags) != 2 || doc.Tags[0] != "go" || doc.Tags[1] != "检索" {
		t.Fatalf("tags = %v, want [go 检索]", doc.Tags)
	}
}

func TestImportRejectsInvalidMetadata(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	manyTags := make([]string, maxTagsPerDocument+1)
	for i := range manyTags {
		manyTags[i] = string(rune('a'+i%26)) + string(rune('0'+i/26))
	}

	cases := []struct {
		name string
		in   NoteInput
	}{
		{"tag 带逗号", NoteInput{Content: "正文", Tags: []string{"a,b"}}},
		{"tag 超长", NoteInput{Content: "正文", Tags: []string{strings.Repeat("好", maxTagRunes+1)}}},
		{"tag 过多", NoteInput{Content: "正文", Tags: manyTags}},
		{"分类超长", NoteInput{Content: "正文", Collection: strings.Repeat("类", maxCollectionRunes+1)}},
	}
	for _, c := range cases {
		if _, _, err := svc.ImportNote(context.Background(), c.in); !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("%s: err = %v, want ErrInvalidMetadata", c.name, err)
		}
	}
}

func TestListFilters(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	ctx := context.Background()
	seeds := []NoteInput{
		{Content: "a 号", Collection: "工作", Tags: []string{"go"}},
		{Content: "b 号", Collection: "工作", Tags: []string{"检索"}},
		{Content: "c 号", Tags: []string{"go"}},
	}
	for _, seed := range seeds {
		if _, _, err := svc.ImportNote(ctx, seed); err != nil {
			t.Fatalf("seed %q: %v", seed.Content, err)
		}
	}

	collection := "工作"
	docs, total, err := svc.List(ctx, DocumentFilter{Collection: &collection}, 10, 0)
	if err != nil {
		t.Fatalf("filter by collection: %v", err)
	}
	if len(docs) != 2 || total != 2 {
		t.Errorf("collection filter = %d docs / total %d, want 2/2", len(docs), total)
	}

	tag := "go"
	docs, total, err = svc.List(ctx, DocumentFilter{Tag: &tag}, 10, 0)
	if err != nil {
		t.Fatalf("filter by tag: %v", err)
	}
	if len(docs) != 2 || total != 2 {
		t.Errorf("tag filter = %d docs / total %d, want 2/2", len(docs), total)
	}

	status := StatusReady
	if _, total, err = svc.List(ctx, DocumentFilter{Status: &status}, 10, 0); err != nil || total != 3 {
		t.Errorf("status filter total = %d err = %v, want 3", total, err)
	}

	// 两个条件同时生效，总数必须跟着收窄——否则前端算出来的页数是错的。
	docs, total, err = svc.List(ctx, DocumentFilter{Collection: &collection, Tag: &tag}, 10, 0)
	if err != nil {
		t.Fatalf("combined filter: %v", err)
	}
	if len(docs) != 1 || total != 1 {
		t.Errorf("combined filter = %d docs / total %d, want 1/1", len(docs), total)
	}
}

func TestUpdateMetadata(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	ctx := context.Background()
	doc, _, err := svc.ImportNote(ctx, NoteInput{Content: "# 原标题\n\n正文。\n"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	title, collection := "  新标题  ", "归档"
	tags := []string{"x", "x", "y"}
	updated, err := svc.Update(ctx, doc.ID, UpdateInput{Title: &title, Collection: &collection, Tags: &tags})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != "新标题" {
		t.Errorf("title = %q, want 新标题", updated.Title)
	}
	if updated.Collection == nil || *updated.Collection != "归档" {
		t.Errorf("collection = %v, want 归档", updated.Collection)
	}
	if len(updated.Tags) != 2 || updated.Tags[0] != "x" || updated.Tags[1] != "y" {
		t.Errorf("tags = %v, want [x y]", updated.Tags)
	}

	// 空串 / 空数组是"清空"，不是"不改"。
	empty := ""
	cleared, err := svc.Update(ctx, doc.ID, UpdateInput{Collection: &empty, Tags: &[]string{}})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Collection != nil {
		t.Errorf("collection = %v, want nil after clearing", cleared.Collection)
	}
	if len(cleared.Tags) != 0 {
		t.Errorf("tags = %v, want empty after clearing", cleared.Tags)
	}
	// 标题没传，就不能被顺手改成空。
	if cleared.Title != "新标题" {
		t.Errorf("title = %q, untouched fields must keep their value", cleared.Title)
	}
}

func TestUpdateRejectsInvalidInput(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	ctx := context.Background()
	doc, _, err := svc.ImportNote(ctx, NoteInput{Content: "正文"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	blank := "   "
	if _, err := svc.Update(ctx, doc.ID, UpdateInput{Title: &blank}); !errors.Is(err, ErrInvalidMetadata) {
		t.Errorf("blank title: err = %v, want ErrInvalidMetadata", err)
	}
	if _, err := svc.Update(ctx, doc.ID, UpdateInput{}); !errors.Is(err, ErrInvalidMetadata) {
		t.Errorf("empty patch: err = %v, want ErrInvalidMetadata", err)
	}
	title := "无所谓"
	if _, err := svc.Update(ctx, 999, UpdateInput{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing document: err = %v, want ErrNotFound", err)
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

	docs, total, err := svc.List(ctx, DocumentFilter{}, 0, -5)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(docs) != 3 {
		t.Fatalf("total = %d, len = %d, want 3/3", total, len(docs))
	}

	docs, _, err = svc.List(ctx, DocumentFilter{}, 2, 1)
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

func TestDeleteRemovesDocumentChunksAndFile(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	doc, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader("# 标题\n\n正文。\n"),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	path := *doc.StoragePath

	if err := svc.Delete(context.Background(), doc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(repo.docs) != 0 {
		t.Errorf("documents = %d, want 0", len(repo.docs))
	}
	if len(repo.chunks[doc.ID]) != 0 {
		t.Errorf("chunks left behind: %d", len(repo.chunks[doc.ID]))
	}
	if _, ok := store.files[path]; ok {
		t.Error("stored file must be removed after the row is gone")
	}
	if len(store.removed) != 1 {
		t.Errorf("removed = %v, want exactly one path", store.removed)
	}
}

func TestDeleteMissingDocument(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	if err := svc.Delete(context.Background(), 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// 删库成功、删文件失败：用户看到的是"已删除"，所以不能回滚也不能报错，
// 只留一条 warn。这里断言"不报错且文件仍在"，正是为了让这个取舍可见。
func TestDeleteSucceedsWhenFileRemovalFails(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	doc, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader("# 标题\n\n正文。\n"),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	store.removeErr = errors.New("permission denied")

	if err := svc.Delete(context.Background(), doc.ID); err != nil {
		t.Fatalf("delete must not fail: %v", err)
	}
	if len(repo.docs) != 0 {
		t.Error("row must stay deleted even when the file cannot be removed")
	}
	if _, ok := store.files[*doc.StoragePath]; !ok {
		t.Error("file should still exist; removal failed")
	}
}

// 重索引从原文重新切分：不是"再切一份追加"，而是整体替换。
func TestReindexRecomputesChunksFromStoredFile(t *testing.T) {
	svc, repo, store := newTestService(1 << 20)
	doc, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader("# 旧标题\n\n旧正文。\n"),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	oldChunkCount := len(repo.chunks[doc.ID])

	newText := "# 新标题\n\n新正文第一段。\n\n## 新小节\n\n新正文第二段。\n"
	store.files[*doc.StoragePath] = []byte(newText)

	updated, err := svc.Reindex(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if updated.ChunkCount != len(repo.chunks[doc.ID]) {
		t.Errorf("chunk_count = %d, stored = %d", updated.ChunkCount, len(repo.chunks[doc.ID]))
	}
	if len(repo.chunks[doc.ID]) == oldChunkCount && oldChunkCount == 1 {
		// 新内容有两个章节，切出来的块数理应变化；固定成 1 说明没重算。
		t.Error("chunks were not recomputed from the new source")
	}
	// 偏移必须仍然能切回新原文（这是"重索引后引用还能定位"的前提）。
	runes := []rune(newText)
	for i, c := range repo.chunks[doc.ID] {
		if c.Seq != i {
			t.Errorf("chunk %d seq = %d", i, c.Seq)
		}
		if got := string(runes[c.StartOffset:c.EndOffset]); got != c.Content {
			t.Errorf("chunk %d offsets do not map back to the source", i)
		}
	}
	last := repo.chunks[doc.ID][len(repo.chunks[doc.ID])-1]
	if last.Heading == nil || *last.Heading != "新小节" {
		t.Errorf("last chunk heading = %v, want 新小节", last.Heading)
	}
}

// 替换失败时旧分块必须原封不动，否则用户重试前会看到一条空文档。
func TestReindexKeepsOldChunksWhenReplaceFails(t *testing.T) {
	svc, repo, _ := newTestService(1 << 20)
	doc, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader("# 标题\n\n正文。\n"),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	before := repo.chunks[doc.ID]

	repo.replaceErr = errors.New("database is down")
	if _, err := svc.Reindex(context.Background(), doc.ID); err == nil {
		t.Fatal("expected reindex to fail")
	}
	if len(repo.chunks[doc.ID]) != len(before) {
		t.Fatalf("old chunks changed: %d -> %d", len(before), len(repo.chunks[doc.ID]))
	}
}

func TestReindexMissingSource(t *testing.T) {
	svc, _, store := newTestService(1 << 20)
	doc, _, err := svc.ImportFile(context.Background(), FileInput{
		Filename: "a.md",
		Content:  strings.NewReader("# 标题\n\n正文。\n"),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	delete(store.files, *doc.StoragePath) // 原文被手工删掉

	if _, err := svc.Reindex(context.Background(), doc.ID); !errors.Is(err, ErrSourceMissing) {
		t.Fatalf("err = %v, want ErrSourceMissing", err)
	}
}

func TestReindexMissingDocument(t *testing.T) {
	svc, _, _ := newTestService(1 << 20)
	if _, err := svc.Reindex(context.Background(), 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// 笔记没有文件，重索引读的是 documents.content。
func TestReindexNoteUsesStoredContent(t *testing.T) {
	svc, repo, _ := newTestService(1 << 20)
	doc, _, err := svc.ImportNote(context.Background(), NoteInput{
		Content: "# 标题\n\n正文。\n",
	})
	if err != nil {
		t.Fatalf("import note: %v", err)
	}
	updated, err := svc.Reindex(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if len(repo.chunks[doc.ID]) == 0 || updated.ChunkCount == 0 {
		t.Error("note reindex must produce chunks")
	}
}

// TestStartImportEnqueuesThenProcesses 覆盖 Phase 6 确立的"受理与执行分离"：
// StartFileImport 只写 pending 文档与任务，分块要等 ProcessImport 才出现。
//
// 这个用例是后续异步化的地基——如果受理阶段就把分块写了，"异步"只是换了个
// 调用位置，任务表也失去意义。
func TestStartImportEnqueuesThenProcesses(t *testing.T) {
	svc, repo, _ := newTestService(1 << 20)
	ctx := context.Background()

	res, err := svc.StartFileImport(ctx, FileInput{
		Filename: "note.md",
		Content:  strings.NewReader("# 标题\n\n正文内容。\n"),
	})
	if err != nil {
		t.Fatalf("StartFileImport: %v", err)
	}
	if res.Existed {
		t.Fatal("first import should not be treated as existing")
	}
	if res.JobID == 0 {
		t.Fatal("a job should be enqueued for the new document")
	}
	if res.Document.Status != StatusPending {
		t.Errorf("status = %s, want pending", res.Document.Status)
	}
	if res.Document.ChunkCount != 0 {
		t.Errorf("chunk_count = %d, want 0 before processing", res.Document.ChunkCount)
	}
	if len(repo.jobs) != 1 || repo.jobs[0].Kind != KindDocumentImport {
		t.Fatalf("enqueued jobs = %v, want one %s", repo.jobs, KindDocumentImport)
	}
	// 任务必须指向刚建好的文档，否则 worker 不知道该处理谁。
	if got := repo.jobs[0].Payload["document_id"]; got != res.Document.ID {
		t.Errorf("job payload document_id = %v, want %d", got, res.Document.ID)
	}

	// 执行阶段：真实运行时由 worker 调用，这里直接调，避免测试依赖轮询时间。
	if err := svc.ProcessImport(ctx, res.Document.ID); err != nil {
		t.Fatalf("ProcessImport: %v", err)
	}
	done, err := svc.Get(ctx, res.Document.ID)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if done.Status != StatusReady {
		t.Errorf("status = %s, want ready after processing", done.Status)
	}
	if done.ChunkCount == 0 {
		t.Error("processing should write chunks")
	}
}
