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
	"time"

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
	// chunk_count 是派生值。在这里统一赋值而不是让调用方自己算：
	// 它必须与本次写入的分块数严格一致，放在写入方才能保证不漏、不写错。
	doc.ChunkCount = len(chunks)

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

// GetByContentHash 按内容哈希读取文档。
//
// 幂等导入靠它：写入撞上 content_hash 唯一索引后，用它把"已经存在的那一条"读回来。
// 不用"先查再插"是因为那在并发下有竞态（两个请求同时查不到，随后一个必然插入失败）；
// 而"先插、冲突再查"由数据库的唯一索引保证串行化——冲突方看到的必然是已提交的行。
func (r *Repository) GetByContentHash(ctx context.Context, hash string) (*Document, error) {
	var doc Document
	err := r.db.WithContext(ctx).
		Select(documentColumns).
		First(&doc, "content_hash = ?", hash).Error
	if err != nil {
		return nil, translateError("get document by content hash", err)
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

// Delete 删除文档。分块由外键 ON DELETE CASCADE 一并删除，不需要在这里显式删。
//
// 返回受影响行数是否为 0 来判断"本来就没有这条"：这样并发下两次删除里
// 只有一次成功，另一次得到 ErrNotFound，而不是两边都以为删掉了。
func (r *Repository) Delete(ctx context.Context, id int64) error {
	res := r.db.WithContext(ctx).Delete(&Document{}, "id = ?", id)
	if res.Error != nil {
		return translateError("delete document", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceChunks 用新分块整体替换某个文档的分块，并同步 chunk_count。
//
// 必须在同一事务里"先删后插"：中途失败时如果旧分块已经删掉，文档就变成
// 一条没有任何分块的 ready 记录，检索不到内容也无法解释。
// 事务保证失败后旧数据完整保留，用户重试即可。
func (r *Repository) ReplaceChunks(ctx context.Context, documentID int64, chunks []Chunk) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 不检查存在性的话，"删 0 行 + 插 0 行"也会提交成功，
		// 调用方会误以为重索引跑过了。
		var exists int64
		if err := tx.Model(&Document{}).Where("id = ?", documentID).Count(&exists).Error; err != nil {
			return err
		}
		if exists == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&Chunk{}).Error; err != nil {
			return err
		}
		for i := range chunks {
			chunks[i].DocumentID = documentID
		}
		if len(chunks) > 0 {
			if err := tx.CreateInBatches(chunks, 200).Error; err != nil {
				return err
			}
		}
		// 用 map 而不是结构体更新：结构体会跳过零值字段，
		// 而"新结果恰好是 0 块"是必须真的写进去的值。
		return tx.Model(&Document{}).Where("id = ?", documentID).
			Updates(map[string]any{
				"chunk_count": len(chunks),
				"updated_at":  time.Now().UTC(),
			}).Error
	})
	return translateError("replace chunks", err)
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
