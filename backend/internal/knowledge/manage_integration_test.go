package knowledge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/job"
	"github.com/luo303/mindos/backend/internal/platform"
)

// 本文件覆盖 Phase 5 的文档管理能力在**真实 PostgreSQL** 上的行为：
// 幂等导入靠唯一索引兜底，text[] 的读写靠驱动编码，删除的分块清理靠外键级联，
// 这三件事在内存替身里都测不出真伪。

func strPtr(s string) *string { return &s }

// newIntegrationDocument 造一条笔记型文档：正文存在 content 列，
// 不依赖文件系统，测试失败时也少一层"是盘上的问题还是库里的问题"。
func newIntegrationDocument(text string) *Document {
	content := text
	return &Document{
		Title:       "集成测试文档",
		MIME:        MIMEMarkdown,
		SizeBytes:   int64(len(text)),
		ContentHash: platform.ContentHash([]byte(text)),
		SourceType:  SourceNote,
		Content:     &content,
		Status:      StatusReady,
	}
}

func TestTextArrayRoundTripIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	doc := newIntegrationDocument("数组读写")
	doc.Collection = strPtr("工作")
	// 故意混入空格、引号、中文：这几个字符决定数组字面量要不要加引号、
	// 以及解析时要不要去转义，是最容易写错的地方。
	doc.Tags = StringArray{"go", "含 空格", `含"引号"`, "数据库"}
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(2)); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Collection == nil || *got.Collection != "工作" {
		t.Errorf("collection = %v, want 工作", got.Collection)
	}
	want := []string{"go", "含 空格", `含"引号"`, "数据库"}
	if len(got.Tags) != len(want) {
		t.Fatalf("tags = %v, want %v", got.Tags, want)
	}
	for i := range want {
		if got.Tags[i] != want[i] {
			t.Fatalf("tags[%d] = %q, want %q (full %v)", i, got.Tags[i], want[i], got.Tags)
		}
	}

	// 按标签筛选走 GIN 索引与 ANY(tags)。
	tag := "含 空格"
	docs, err := repo.List(ctx, DocumentFilter{Tag: &tag}, 10, 0)
	if err != nil {
		t.Fatalf("list by tag: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != doc.ID {
		t.Fatalf("list by tag = %+v", docs)
	}
	total, err := repo.Count(ctx, DocumentFilter{Tag: &tag})
	if err != nil || total != 1 {
		t.Fatalf("count by tag = %d err = %v", total, err)
	}

	// 不存在的标签不能报错，只能是空结果。
	missing := "根本没有"
	if docs, err = repo.List(ctx, DocumentFilter{Tag: &missing}, 10, 0); err != nil || len(docs) != 0 {
		t.Fatalf("unknown tag: docs = %v err = %v", docs, err)
	}
}

func TestUpdateMetadataIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	doc := newIntegrationDocument("局部更新")
	doc.Collection = strPtr("工作")
	doc.Tags = StringArray{"go"}
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(1)); err != nil {
		t.Fatalf("create: %v", err)
	}

	title := "改过的标题"
	empty := ""
	tags := StringArray{"检索"}
	if err := repo.Update(ctx, doc.ID, DocumentPatch{
		Title: &title, Collection: &empty, Tags: &tags,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != title {
		t.Errorf("title = %q, want %q", got.Title, title)
	}
	// 空串必须落成 NULL：否则"未分类"在库里会有 NULL 与 '' 两种表示。
	if got.Collection != nil {
		t.Errorf("collection = %v, want nil after clearing", got.Collection)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "检索" {
		t.Errorf("tags = %v, want [检索]", got.Tags)
	}
	if !got.UpdatedAt.After(doc.CreatedAt) && got.UpdatedAt.Equal(doc.CreatedAt) {
		t.Error("updated_at should move forward on update")
	}

	if err := repo.Update(ctx, 999, DocumentPatch{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing doc: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteCascadesChunksIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	doc := newIntegrationDocument("删除级联")
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(3)); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.Delete(ctx, doc.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}

	// 分块由 ON DELETE CASCADE 清理，这里直接查表确认没有孤儿行。
	var orphan int64
	if err := db.Model(&Chunk{}).Where("document_id = ?", doc.ID).Count(&orphan).Error; err != nil {
		t.Fatalf("count orphan chunks: %v", err)
	}
	if orphan != 0 {
		t.Errorf("orphan chunks = %d, want 0", orphan)
	}

	// 重复删除是 404，不是"又成功一次"。
	if err := repo.Delete(ctx, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestReplaceChunksIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	doc := newIntegrationDocument("重索引替换")
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(2)); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.ReplaceChunks(ctx, doc.ID, chunkFixture(3)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := repo.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ChunkCount != 3 {
		t.Errorf("chunk_count = %d, want 3", got.ChunkCount)
	}
	chunks, err := repo.ListChunks(ctx, doc.ID, 10, 0)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	for i, c := range chunks {
		if c.Seq != i {
			t.Errorf("chunks[%d].seq = %d", i, c.Seq)
		}
	}

	// 替换成空集也要真的写进去：0 是合法值，不能被"跳过零值"的逻辑吞掉。
	if err := repo.ReplaceChunks(ctx, doc.ID, nil); err != nil {
		t.Fatalf("replace with none: %v", err)
	}
	got, err = repo.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ChunkCount != 0 {
		t.Errorf("chunk_count = %d, want 0", got.ChunkCount)
	}
}

// 端到端：真仓储 + 内存存储替身，验证"同内容只留一条记录、一份原文"。
func TestIdempotentImportIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	// 存储用替身而不是真 MinIO：这个用例验证的是 service + repository 的幂等链路，
	// 对象存储的真实读写由 platform 的 MinIO 集成测试覆盖。
	// 若这里也要求装 MinIO，跑一次数据库集成测试就得启两个容器。
	store := newFakeStore()
	svc := NewService(NewRepository(db), store, 1<<20, nil)
	ctx := context.Background()
	text := "# 幂等\n\n同一份内容只应该入库一次。\n"

	first, existed, err := importFile(ctx, t, svc, FileInput{
		Filename: "a.md",
		Content:  strings.NewReader(text),
	})
	if err != nil || existed {
		t.Fatalf("first import: existed=%v err=%v", existed, err)
	}

	second, existed, err := importFile(ctx, t, svc, FileInput{
		Filename: "b.md",
		Content:  strings.NewReader(text),
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !existed {
		t.Error("second import must hit the existing document")
	}
	if second.ID != first.ID {
		t.Errorf("second id = %d, want %d", second.ID, first.ID)
	}

	var count int64
	if err := db.Model(&Document{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 1 {
		t.Errorf("documents = %d, want 1", count)
	}
	var chunkTotal int64
	if err := db.Model(&Chunk{}).Count(&chunkTotal).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if int(chunkTotal) != first.ChunkCount {
		t.Errorf("chunks = %d, want %d (duplicate must not re-split)", chunkTotal, first.ChunkCount)
	}

	// 原文仍然可读：幂等路径绝不能把既有文档的文件删掉。
	data, err := store.Read(ctx, *first.StoragePath)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(data) != text {
		t.Errorf("stored file changed: %q", data)
	}
}

// TestReindexTaskIntegration 验证"重索引受理"在真实库上的三件事：
// 状态与任务同一事务、同一文档不能排两条队、执行完状态回到 ready。
func TestReindexTaskIntegration(t *testing.T) {
	db := setupKnowledgeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	doc := newIntegrationDocument("重索引任务")
	if err := repo.CreateWithChunks(ctx, doc, chunkFixture(2)); err != nil {
		t.Fatalf("create: %v", err)
	}

	key := "document:" + strconv.FormatInt(doc.ID, 10) + ":reindex"
	makeJob := func() *job.Job {
		return &job.Job{
			Kind:        KindDocumentReindex,
			Status:      job.StatusQueued,
			DedupeKey:   &key,
			Payload:     job.Payload{},
			MaxAttempts: 3,
			RunAfter:    time.Now().UTC(),
		}
	}

	if err := repo.MarkIndexingWithJob(ctx, doc.ID, makeJob()); err != nil {
		t.Fatalf("mark indexing: %v", err)
	}
	indexing, err := repo.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if indexing.Status != StatusIndexing {
		t.Errorf("status = %s, want indexing", indexing.Status)
	}

	// 部分唯一索引兜底：同一文档第二条未完成的重索引任务被拒。
	if err := repo.MarkIndexingWithJob(ctx, doc.ID, makeJob()); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second mark indexing error = %v, want ErrDuplicate", err)
	}

	if err := repo.FinishImport(ctx, doc.ID, chunkFixture(3)); err != nil {
		t.Fatalf("finish import: %v", err)
	}
	got, err := repo.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusReady || got.ChunkCount != 3 {
		t.Errorf("after finish: status = %s chunk_count = %d, want ready/3", got.Status, got.ChunkCount)
	}
}
