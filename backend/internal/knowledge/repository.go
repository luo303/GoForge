// 本文件是文档模块的数据访问层。
//
// 两条纪律：
//  1. 只用 GORM 的链式 API（`db.WithContext(ctx).Where(...)`），不混用泛型 API；
//  2. **显式列名**。SELECT * 会让以后新增的大列（embedding 1536 维 = 6KB/行）
//     被无意识地读进内存，列表接口返回 20 条就多搬 120KB。
package knowledge

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// Repository 是 documents / document_chunks 的数据访问。
type Repository struct {
	db *gorm.DB
}

// NewRepository 创建仓储。db 由 cmd/server 注入，仓储内部不建连接。
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// documentColumns 是列表与详情读取的列。
//
// 故意不含 source_url / collection / tags / error_*：它们要到 Phase 5/6 才有读写方，
// 现在读出来只会得到零值。列名集中在这里，避免两个方法各自写一份字符串。
var documentColumns = []string{
	"id", "title", "source_type", "mime", "size_bytes", "content_hash",
	"storage_path", "content", "status", "chunk_count", "created_at", "updated_at",
}

// chunkColumns 跳过 embedding 与 tsv：前者 6KB/行且本阶段不用，
// 后者是数据库生成的检索列，给前端看没有意义。
var chunkColumns = []string{
	"id", "document_id", "seq", "content", "heading",
	"start_offset", "end_offset", "token_count", "created_at",
}

// CreateWithChunks 在一个事务里写入文档与它的全部分块。
//
// 为什么必须同一事务：半途失败时"有文档但没有分块"的记录会让页面显示一个
// 空文档，用户无法判断是解析没跑还是内容真为空。事务保证要么都成功、要么都没有。
//
// 分块用 CreateInBatches 而不是逐条 Create：一次 INSERT 多行，把 N 次往返压成
// ceil(N/200) 次；200 是兼顾 SQL 长度与单条语句参数上限的经验值。
func (r *Repository) CreateWithChunks(ctx context.Context, doc *Document, chunks []Chunk) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(doc).Error; err != nil {
			return err
		}
		if len(chunks) == 0 {
			return nil
		}
		// 用下标回填而不是 range 取值：range 拿到的是副本，改它不会影响切片。
		for i := range chunks {
			chunks[i].DocumentID = doc.ID
		}
		return tx.CreateInBatches(chunks, 200).Error
	})
	return translateError("create document", err)
}

// Get 按主键读取文档，不存在时返回 ErrNotFound。
func (r *Repository) Get(ctx context.Context, id int64) (*Document, error) {
	var doc Document
	err := r.db.WithContext(ctx).
		Select(documentColumns).
		First(&doc, "id = ?", id).Error
	if err != nil {
		return nil, translateError("get document", err)
	}
	return &doc, nil
}

// List 按创建时间倒序分页。
func (r *Repository) List(ctx context.Context, limit, offset int) ([]Document, error) {
	docs := make([]Document, 0, limit)
	err := r.db.WithContext(ctx).
		Select(documentColumns).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&docs).Error
	if err != nil {
		return nil, translateError("list documents", err)
	}
	return docs, nil
}

// Count 返回文档总数，供分页使用。
func (r *Repository) Count(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&Document{}).Count(&total).Error; err != nil {
		return 0, translateError("count documents", err)
	}
	return total, nil
}

// ListChunks 按 seq 升序返回某个文档的分块。
func (r *Repository) ListChunks(ctx context.Context, documentID int64, limit, offset int) ([]Chunk, error) {
	chunks := make([]Chunk, 0, limit)
	err := r.db.WithContext(ctx).
		Select(chunkColumns).
		Where("document_id = ?", documentID).
		Order("seq ASC").
		Limit(limit).
		Offset(offset).
		Find(&chunks).Error
	if err != nil {
		return nil, translateError("list chunks", err)
	}
	return chunks, nil
}

// CountChunks 返回某个文档的分块数。
func (r *Repository) CountChunks(ctx context.Context, documentID int64) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&Chunk{}).
		Where("document_id = ?", documentID).
		Count(&total).Error
	if err != nil {
		return 0, translateError("count chunks", err)
	}
	return total, nil
}

// translateError 把 GORM 的错误翻译成模块内的哨兵错误。
//
// TranslateError 已在 platform.NewPostgres 的 gorm.Config 里打开，
// 所以唯一约束冲突会得到 gorm.ErrDuplicatedKey，不需要去解析 PG 的 23505 字符串。
// 其余错误保留原始信息（用 %w 包住），方便日志定位具体是哪条 SQL 失败。
func translateError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return ErrDuplicate
	default:
		return fmt.Errorf("knowledge: %s: %w", op, err)
	}
}
