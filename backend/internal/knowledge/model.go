// Package knowledge 是知识模块：把文件、笔记、网页变成可检索的分块。
//
// 分层与项目约定一致：
//
//	handler.go    HTTP 绑定、参数校验、错误码映射
//	service.go    业务规则：类型/大小校验、事务边界、落盘与清理
//	repository.go GORM 数据访问（只收 context + 参数）
//	parser.go     原文 → 结构块（标题 / 段落 / 代码块），保留字节偏移
//	chunker.go    结构块 → 分块，保留码点偏移
//	model.go      documents / document_chunks 的 Go 视图（本文件）
//
// 模块边界：这里不认识 gin 的 Context、不拼 HTTP 响应；错误用本包的
// 哨兵错误（ErrNotFound 等）表达，由 handler 翻译成 httpx 错误码。
package knowledge

import "time"

// 文档来源类型，对应 documents.source_type 的 CHECK 约束。
const (
	SourceFile = "file"
	SourceNote = "note"
	SourceWeb  = "web" // Phase 5 起使用
)

// 文档状态，对应 documents.status 的 CHECK 约束。
//
// Phase 4 是同步导入：解析与切分在请求内完成，成功即 ready，失败整笔事务回滚，
// 所以这一阶段只会看到 ready。其余状态留给 Phase 6 的异步任务（pending/parsing/
// indexing/failed/cancelling）；先把常量定义齐，是为了让状态机的完整含义有落点。
const (
	StatusPending    = "pending"
	StatusParsing    = "parsing"
	StatusIndexing   = "indexing"
	StatusReady      = "ready"
	StatusFailed     = "failed"
	StatusCancelling = "cancelling"
)

// Document 对应 documents 表。
//
// 只声明本阶段真正读写的列。表里还有 source_url / error_code / error_message，
// 它们分别属于网页抓取（已移出主线）与 Phase 6 的异步任务失败信息，
// 现在加到结构体里只会得到"永远是零值"的字段，读代码的人还要多判断一次
// "这个字段到底从哪来"。需要时再加，加字段不需要改表。
type Document struct {
	ID        int64  `gorm:"column:id;primaryKey"`
	Title     string `gorm:"column:title"`
	MIME      string `gorm:"column:mime"`
	SizeBytes int64  `gorm:"column:size_bytes"`
	// ContentHash 是 SHA-256 十六进制串，重复导入的判定依据（唯一索引）。
	ContentHash string `gorm:"column:content_hash"`
	SourceType  string `gorm:"column:source_type"`
	// StoragePath 是相对 STORAGE_DIR 的路径，null 表示没有原始文件（笔记）。
	StoragePath *string `gorm:"column:storage_path"`
	// Content 只对笔记有值；文件的正文不重复入库，需要时从 StoragePath 读。
	Content *string `gorm:"column:content"`
	// Collection 是单值分类，nil 表示未分类。不做成外键表：单用户场景下
	// 分类只是一层筛选标签，独立建表换来的只有一次 JOIN。
	Collection *string `gorm:"column:collection"`
	// Tags 对应 text[]。必须写下 type:text[]，否则 GORM 无法推断列类型
	// （见 string_array.go 顶部的实测记录）。
	Tags       StringArray `gorm:"column:tags;type:text[]"`
	Status     string      `gorm:"column:status"`
	ChunkCount int         `gorm:"column:chunk_count"`
	CreatedAt  time.Time   `gorm:"column:created_at"`
	UpdatedAt  time.Time   `gorm:"column:updated_at"`
}

// TableName 显式声明表名。GORM 的默认复数化对 document 恰好正确，
// 但显式写出来可以避免以后改结构体名时悄悄换表。
func (Document) TableName() string { return "documents" }

// Chunk 对应 document_chunks 表。
//
// 偏移是**码点**偏移，不是字节。原文可以用它精确切片：
//
//	runes := []rune(text); runes[c.StartOffset:c.EndOffset]
//
// 为什么不在结构体里放 Embedding：Phase 4 不写也不读向量，1536 维 float32
// 每次查询都会白白多搬 6KB；等 Phase 7 需要时再加。
type Chunk struct {
	ID         int64  `gorm:"column:id;primaryKey"`
	DocumentID int64  `gorm:"column:document_id"`
	Seq        int    `gorm:"column:seq"`
	Content    string `gorm:"column:content"`
	// Heading 是分块所在章节的最近一级标题，文首（第一个标题之前）为 nil。
	Heading     *string   `gorm:"column:heading"`
	StartOffset int       `gorm:"column:start_offset"`
	EndOffset   int       `gorm:"column:end_offset"`
	TokenCount  int       `gorm:"column:token_count"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// TableName 见 Document.TableName 的说明。
func (Chunk) TableName() string { return "document_chunks" }
