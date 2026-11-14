// 本文件是知识模块的业务层：校验、编排、事务边界。
//
// 它不 import gin，也不拼 HTTP 响应；失败用本包的哨兵错误表示，
// 由 handler 翻译成统一错误码。这样同一段业务逻辑将来被 worker 复用时，
// 不需要跟着改错误处理。
package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/job"
	"github.com/luo303/mindos/backend/internal/platform"
)

// 模块级哨兵错误。handler 用 errors.Is 判断，不要比较错误字符串。
var (
	// ErrEmptyDocument 表示正文去掉空白后为空——空文档没有可切分的内容。
	ErrEmptyDocument = errors.New("document is empty")
	// ErrNotUTF8 表示内容不是合法 UTF-8（例如误传了 PDF 或压缩包）。
	ErrNotUTF8 = errors.New("document is not valid UTF-8 text")
	// ErrTooLarge 表示超过 STORAGE_MAX_UPLOAD_BYTES。
	ErrTooLarge = errors.New("document exceeds the upload size limit")
	// ErrDuplicate 表示同内容的文档已存在（content_hash 唯一索引冲突）。
	ErrDuplicate = errors.New("document content already exists")
	// ErrNotFound 表示文档不存在。
	ErrNotFound = errors.New("document not found")
	// ErrSourceMissing 表示文档的原文已不可得（文件被删、笔记没有正文），
	// 重索引无法进行。这不是"参数错"，而是数据状态已经不可解释。
	ErrSourceMissing = errors.New("document source is missing")
	// ErrInvalidMetadata 表示分类或标签不合法（超长、含分隔符、数量过多）。
	ErrInvalidMetadata = errors.New("invalid document metadata")
	// ErrStorageUnavailable 表示读原文时对象存储不可用（连不上、超时）。
	//
	// 与 ErrSourceMissing 的区别很重要：原文缺失是数据状态坏了，重试没有意义；
	// 存储不可用是暂时故障，应该重试。两者混成一个错误，就会出现
	// "MinIO 重启一下就能好的任务被判成永久失败"。
	ErrStorageUnavailable = errors.New("document storage is unavailable")
	// ErrNoProcessing 表示该文档当前没有进行中的处理可以取消。
	ErrNoProcessing = errors.New("no processing in progress")
	// ErrReindexRunning 表示同一文档已有未完成的重索引任务。
	// 与"重复导入"不同，重复导入返回既有文档（幂等），而重索引是"再排队一次"
	// 的意图——已经排着队了就没必要再来一条，直接告诉调用方在跑。
	ErrReindexRunning = errors.New("document reindex is already running")
)

// 分页边界。上限 100 是"防止一次拉全库"的护栏，不是性能优化。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// documentRepository 是 Service 需要的数据访问能力。
//
// 接口定义在**使用方**而不是 Repository 一侧：Go 的接口是隐式实现的，
// *Repository 什么都不用写就满足它；而测试可以塞一个内存实现，
// 不需要数据库。这是本项目"接口按需定义"的常规做法。
type documentRepository interface {
	CreateWithChunks(ctx context.Context, doc *Document, chunks []Chunk) error
	Get(ctx context.Context, id int64) (*Document, error)
	GetByContentHash(ctx context.Context, hash string) (*Document, error)
	List(ctx context.Context, filter DocumentFilter, limit, offset int) ([]Document, error)
	Count(ctx context.Context, filter DocumentFilter) (int64, error)
	ListChunks(ctx context.Context, documentID int64, limit, offset int) ([]Chunk, error)
	CountChunks(ctx context.Context, documentID int64) (int64, error)
	Update(ctx context.Context, id int64, patch DocumentPatch) error
	Delete(ctx context.Context, id int64) error
	ReplaceChunks(ctx context.Context, documentID int64, chunks []Chunk) error
	// CreateWithJob 在一个事务里写待处理文档与它的任务（Phase 6）。
	CreateWithJob(ctx context.Context, doc *Document, j *job.Job) error
	// FinishImport 在同一事务里替换分块并把文档置 ready（Phase 6）。
	FinishImport(ctx context.Context, documentID int64, chunks []Chunk) error
	// MarkIndexingWithJob 在同一事务里把文档置 indexing 并建重索引任务（Phase 6）。
	MarkIndexingWithJob(ctx context.Context, documentID int64, j *job.Job) error
	// MarkFailed 记录处理最终失败与原因（Phase 6）。
	MarkFailed(ctx context.Context, documentID int64, code, message string) error
	// MarkCancelling 把文档置为"取消中"，等 worker 停下后写终态。
	MarkCancelling(ctx context.Context, documentID int64) error
	// MarkReady 把文档置回 ready（重索引被取消时用）。
	MarkReady(ctx context.Context, documentID int64) error
}

// jobBus 是本模块需要任务系统的全部能力：唤醒与取消。
//
// 定义在消费方（本包）而不是直接用 job.Repository + job.Notifier：
// service 只用两个方法，依赖窄接口能让测试替身少写一堆空方法，
// 也让"knowledge 到底用了任务系统的哪部分"一目了然。
type jobBus interface {
	// Notify 投递一条已落库的任务（投递失败由补投兜底，不影响业务结果）。
	Notify(ctx context.Context, jobID int64) error
	// RequestCancel 取消某个业务对象未完成的任务。
	RequestCancel(ctx context.Context, documentID int64) (jobID int64, kind job.Kind, status job.Status, err error)
}

// fileStore 是原始文件的存储能力。
//
// 只声明本模块真正调用的三个方法。Read 到 Phase 5 有了重索引才加进来——
// 接口窄一点，测试替身就少写一个空方法，也少一处"这个方法到底谁在用"的疑问。
type fileStore interface {
	Save(ctx context.Context, data []byte) (platform.SavedFile, error)
	Read(ctx context.Context, relPath string) ([]byte, error)
	Remove(ctx context.Context, relPath string) error
}

// Service 编排导入流程：校验 → 解析 → 切分 → 落盘 → 落库。
type Service struct {
	repo documentRepository
	// jobs 负责"落库之后叫醒 worker"与"取消进行中的处理"。
	// 建任务仍然走 repository 的同事务方法：文档与任务必须一起写，
	// 走 job.Repository 会变成两个事务。
	jobs           jobBus
	store          fileStore
	maxUploadBytes int64
	log            *zap.Logger
}

// NewService 组装业务层。maxUploadBytes 来自配置，<=0 时会被兜底成 10 MiB，
// 避免调用方忘了传导致"任何文件都超限"。
//
// log 允许为 nil（测试里常见）：此时退化成不记录。业务逻辑本身不依赖日志，
// 但"删库成功、删文件失败"这种只能记一笔的情况需要它，所以不做成可选参数
// 之外的另一套 API。
func NewService(repo documentRepository, store fileStore, jobs jobBus, maxUploadBytes int64, log *zap.Logger) *Service {
	if maxUploadBytes <= 0 {
		maxUploadBytes = 10 << 20
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{
		repo:           repo,
		store:          store,
		jobs:           jobs,
		maxUploadBytes: maxUploadBytes,
		log:            log,
	}
}

// FileInput 是一次文件导入请求。
//
// Collection / Tags 属于"元信息"，与正文分开传：正文来自文件流，
// 元信息来自表单字段或查询参数，混在一起会让调用方以为标签是文件内容的一部分。
type FileInput struct {
	Filename   string
	Content    io.Reader
	Collection string
	Tags       []string
}

// NoteInput 是一次笔记导入请求。
type NoteInput struct {
	Title      string
	Content    string
	Collection string
	Tags       []string
}

// UpdateInput 是一次局部更新请求。nil 表示"这次不改这一项"，
// 传空串/空切片则表示"清空它"——这两种意图不能合并。
type UpdateInput struct {
	Title      *string
	Collection *string
	Tags       *[]string
}

// 任务类型。取值属于业务语义，所以定义在业务包而不是 job 包——
// job 是基础设施，不该知道"文档"这种概念，否则 Phase 16 复用它会很别扭。
const (
	KindDocumentImport  job.Kind = "document.import"
	KindDocumentReindex job.Kind = "document.reindex"
)

// defaultMaxAttempts 是任务默认重试上限：一次原样重试 + 一次退避后重试。
// 解析类错误（编码不对、类型不支持）重试也没用，那类错误会在 handler 里
// 直接判失败，不会真的跑满三次。
const defaultMaxAttempts = 3

// ImportResult 是一次"导入受理"的结果。
//
// 与旧返回值（文档 + existed）的区别：多了一个 JobID。
// 异步化以后，handler 能立刻告诉用户的只有"我收下了，任务号是多少"，
// 真正的结果要等 worker 跑完才能查。
type ImportResult struct {
	Document *Document
	// JobID 是排队等待执行的任务 id；Existed 为 true 时没有新建任务，值为 0。
	JobID int64
	// Existed 表示"同内容的文档已存在，本次没有写入任何新记录"。
	Existed bool
}

// StartFileImport 受理一次文件导入：校验 → 存原文 → 写待处理文档 + 任务。
//
// 幂等语义与同步版本一致：重复导入不会更新标题、标签或分块，改这些走 PATCH。
// 理由是"同内容 = 同一条记录"——如果第二次上传能改第一次的结果，这条记录
// 就不再由内容唯一决定，existed 也失去意义。
//
// 注意这里仍然会做一次 normalizeText：类型与编码错误属于"用户传错了"，
// 必须在请求里就报给用户，不能收了 202 再在后台失败。
func (s *Service) StartFileImport(ctx context.Context, in FileInput) (*ImportResult, error) {
	mime, ok := MIMEByFilename(in.Filename)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, filepath.Ext(in.Filename))
	}

	data, err := readAllLimited(in.Content, s.maxUploadBytes)
	if err != nil {
		return nil, err
	}
	// 只做校验与去 BOM，不保留正文：切分是后台的事，这里多留一份没有意义。
	if _, data, err = normalizeText(data); err != nil {
		return nil, err
	}
	// 元信息在落盘之前校验：参数不合法时不该先往对象存储写一份再删掉。
	collection, tags, err := normalizeMetadata(in.Collection, in.Tags)
	if err != nil {
		return nil, err
	}

	// 先落盘再落库：对象存储不在数据库事务里，顺序反过来会出现
	// "事务已提交但原文没写成功"——那才是真正不可修复的坏状态。
	saved, err := s.store.Save(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("save document file: %w", err)
	}

	doc := &Document{
		Title:       titleFromFilename(in.Filename),
		MIME:        mime,
		SizeBytes:   int64(len(data)),
		ContentHash: saved.Hash,
		SourceType:  SourceFile,
		StoragePath: &saved.RelPath,
		Collection:  collection,
		Tags:        tags,
		// 状态由 worker 推进：pending → ready / failed。
		Status: StatusPending,
	}
	return s.startImport(ctx, doc, &saved)
}

// StartNoteImport 受理一条笔记。
//
// 笔记的正文存在 documents.content，同时也切分成分块：检索与引用走同一套逻辑，
// 不需要为笔记单独开一条查询路径。
//
// 笔记一律按 Markdown 解析：用户自己写的东西里出现 "# 小节" 是常见用法，
// 而纯文本文件里以 # 开头的行更可能是日志，所以两者规则不同。
func (s *Service) StartNoteImport(ctx context.Context, in NoteInput) (*ImportResult, error) {
	if int64(len(in.Content)) > s.maxUploadBytes {
		return nil, ErrTooLarge
	}
	text, data, err := normalizeText([]byte(in.Content))
	if err != nil {
		return nil, err
	}
	collection, tags, err := normalizeMetadata(in.Collection, in.Tags)
	if err != nil {
		return nil, err
	}

	doc := &Document{
		Title:       titleForNote(in.Title, text),
		MIME:        MIMEMarkdown,
		SizeBytes:   int64(len(data)),
		ContentHash: platform.ContentHash(data),
		SourceType:  SourceNote,
		Content:     &text,
		Collection:  collection,
		Tags:        tags,
		Status:      StatusPending,
	}
	return s.startImport(ctx, doc, nil)
}

// ProcessImport 是任务的执行段：读原文 → 切分 → 替换分块 → 置 ready。
//
// 三条约定（与 job.HandlerFunc 的契约一致）：
//   - 响应 ctx：取消与停止靠它传播；
//   - 可重跑：FinishImport 是"先删后插"的一个事务，重复执行结果不变；
//   - 不碰任务状态：成功/失败由 worker 统一写回。
func (s *Service) ProcessImport(ctx context.Context, documentID int64) error {
	doc, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return err
	}
	text, err := s.sourceText(ctx, doc)
	if err != nil {
		return err
	}
	chunks, err := buildChunks(doc.MIME, text)
	if err != nil {
		return err
	}
	return s.repo.FinishImport(ctx, documentID, chunks)
}

// ProcessImportTask 是 worker 调用的入口：执行导入，并在"最终失败"时写失败原因。
//
// 为什么要这一层，而不是让 worker 直接调 ProcessImport：
// 只有业务层知道哪些错误是"再试也没用"（文件类型不支持、编码不对、原文丢了），
// 也只有业务层能把失败原因写回 documents。worker 只管调度，不该认识这些细节。
//
// attempts / maxAttempts 由 worker 传入，与它自己的重试判断共用同一个结论，
// 避免出现"任务还在重试、文档已经标成失败"的矛盾状态。
func (s *Service) ProcessImportTask(ctx context.Context, documentID int64, attempts, maxAttempts int) error {
	return s.settle(ctx, documentID, s.ProcessImport(ctx, documentID), attempts, maxAttempts)
}

// ProcessReindexTask 是重索引任务的入口，职责同 ProcessImportTask。
func (s *Service) ProcessReindexTask(ctx context.Context, documentID int64, attempts, maxAttempts int) error {
	return s.settle(ctx, documentID, s.ProcessReindex(ctx, documentID), attempts, maxAttempts)
}

// codedError 给错误附加机器可读码，让任务表与文档表记同一个原因。
//
// job 包只认识"有没有码"，不认识具体有哪些码——那属于业务语义，
// 放在基础设施里会让它必须知道文档、记忆、Agent 各自的错误分类。
type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string        { return e.err.Error() }
func (e *codedError) Unwrap() error        { return e.err }
func (e *codedError) JobErrorCode() string { return e.code }

// settle 负责"这次失败之后要不要给文档标红"。
func (s *Service) settle(ctx context.Context, documentID int64, runErr error, attempts, maxAttempts int) error {
	if runErr == nil {
		return nil
	}
	err := s.classify(runErr)
	// 无论重试还是最终失败，都把码附上：worker 会把它写进 jobs.last_error_code，
	// 与 documents.error_code 保持一致，排查时两张表说的是同一件事。
	coded := &codedError{code: errorCode(err), err: err}

	probe := job.Job{Attempts: attempts, MaxAttempts: maxAttempts}
	if job.ShouldRetry(&probe, err) {
		// 还会重试：文档保持当前状态（pending / indexing），
		// 万一这次失败被下一轮成功覆盖，用户根本不需要看到中间的失败。
		return coded
	}
	if markErr := s.repo.MarkFailed(ctx, documentID, coded.code, err.Error()); markErr != nil {
		s.log.Warn("mark document failed",
			zap.Int64("document_id", documentID), zap.Error(markErr))
	}
	return coded
}

// classify 把"再试也没用"的错误标记成永久失败。
//
// 这些错误的共同点：同样的输入、同样的代码，重试三次得到的还是同一个结果。
// 让它们跑满重试次数只会延迟用户看到错误的时间，并浪费三次任务名额。
func (s *Service) classify(err error) error {
	switch {
	case errors.Is(err, ErrUnsupportedType),
		errors.Is(err, ErrEmptyDocument),
		errors.Is(err, ErrNotUTF8),
		errors.Is(err, ErrInvalidMetadata),
		errors.Is(err, ErrSourceMissing),
		errors.Is(err, ErrNotFound):
		return job.Permanent(err)
	default:
		return err
	}
}

// errorCode 把错误翻译成稳定的机器可读代码，写进 documents.error_code。
//
// 用固定字符串而不是错误文案：文案会改、会翻译，前端与排查脚本不该跟着变。
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrUnsupportedType):
		return "unsupported_type"
	case errors.Is(err, ErrEmptyDocument):
		return "empty_document"
	case errors.Is(err, ErrNotUTF8):
		return "not_utf8"
	case errors.Is(err, ErrInvalidMetadata):
		return "invalid_metadata"
	case errors.Is(err, ErrSourceMissing):
		return "source_missing"
	case errors.Is(err, ErrStorageUnavailable):
		// 与 source_missing 分开：这是"存储暂时连不上"，重试或恢复 MinIO 后能好；
		// 前者是原文真的没了，需要用户重新导入。
		return "storage_unavailable"
	case errors.Is(err, ErrNotFound):
		return "document_not_found"
	default:
		return "process_failed"
	}
}

// startImport 写"待处理文档 + 任务"，并把"同内容已存在"翻译成 Existed。
//
// 三个分支的顺序有讲究：
//  1. 成功 → 新建；
//  2. 唯一索引冲突（ErrDuplicate）→ 读回既有记录，**不删文件**。
//     此时对象存储里的正是那条既有文档指向的原文（内容寻址下 key 由哈希决定），
//     删掉会让它变成"有记录没原文"，之后重索引就再也跑不起来；
//  3. 其他失败 → 补偿清理，且只在文件是本次新写的时候删（Reused=false）。
func (s *Service) startImport(ctx context.Context, doc *Document, saved *platform.SavedFile) (*ImportResult, error) {
	// 去重用内容哈希而不是文档 id：id 要等插入之后才有，
	// 而"同一份内容同一时刻只该有一个导入任务"在插入前就得成立。
	dedupeKey := fmt.Sprintf("document:%s:import", doc.ContentHash)
	queued := &job.Job{
		Kind:        KindDocumentImport,
		Status:      job.StatusQueued,
		DedupeKey:   &dedupeKey,
		Payload:     job.Payload{},
		MaxAttempts: defaultMaxAttempts,
		RunAfter:    time.Now().UTC(),
	}

	err := s.repo.CreateWithJob(ctx, doc, queued)
	if err == nil {
		// 落库之后才叫醒：顺序反过来会让 worker 领到一个还不存在的任务。
		// 投递失败不影响本次响应——任务已在表里，补投会重发。
		s.notify(ctx, queued.ID)
		return &ImportResult{Document: doc, JobID: queued.ID}, nil
	}
	if errors.Is(err, ErrDuplicate) {
		existing, getErr := s.repo.GetByContentHash(ctx, doc.ContentHash)
		if getErr == nil {
			return &ImportResult{Document: existing, Existed: true}, nil
		}
		// 撞了唯一索引却读不回来，说明这条记录在冲突与读取之间被删了。
		// 这种情况极少见，退回冲突语义（409）比假装成功更诚实。
		return nil, err
	}
	if saved != nil && !saved.Reused {
		// 两个错误一起返回：清理失败不该掩盖真正的失败原因，
		// 但也不能悄悄吞掉（errors.Join 会保留两条链，日志里都看得到）。
		if rmErr := s.store.Remove(ctx, saved.RelPath); rmErr != nil {
			return nil, errors.Join(err, fmt.Errorf("cleanup stored file: %w", rmErr))
		}
	}
	return nil, err
}

// Get 读取文档详情。
func (s *Service) Get(ctx context.Context, id int64) (*Document, error) {
	return s.repo.Get(ctx, id)
}

// List 分页返回文档，同时给出总数供前端翻页。
//
// 总数与列表用同一个 filter，否则会出现"筛出 3 条、总数却写 10"，
// 前端按总数算出来的页数完全对不上。
func (s *Service) List(ctx context.Context, filter DocumentFilter, limit, offset int) ([]Document, int64, error) {
	limit, offset = ClampPaging(limit, offset)
	docs, err := s.repo.List(ctx, filter, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.Count(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return docs, total, nil
}

// Update 修改文档的元信息（标题、分类、标签），不涉及正文与分块。
//
// 正文与它的分块、哈希是绑在一起的：改正文等于换一份文档，
// 那应该重新导入，而不是在这里悄悄改掉 content_hash 让幂等判定失效。
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Document, error) {
	patch := DocumentPatch{}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return nil, fmt.Errorf("%w: title must not be blank", ErrInvalidMetadata)
		}
		title = truncateRunes(title, 200)
		patch.Title = &title
	}
	if in.Collection != nil {
		collection := strings.TrimSpace(*in.Collection)
		if utf8.RuneCountInString(collection) > maxCollectionRunes {
			return nil, fmt.Errorf("%w: collection must be at most %d characters",
				ErrInvalidMetadata, maxCollectionRunes)
		}
		patch.Collection = &collection
	}
	if in.Tags != nil {
		tags, err := normalizeTags(*in.Tags)
		if err != nil {
			return nil, err
		}
		patch.Tags = &tags
	}
	if patch.Title == nil && patch.Collection == nil && patch.Tags == nil {
		return nil, fmt.Errorf("%w: at least one of title, collection or tags is required",
			ErrInvalidMetadata)
	}

	if err := s.repo.Update(ctx, id, patch); err != nil {
		return nil, err
	}
	// 回读而不是在内存里拼一个：updated_at 由数据库写入，
	// 直接返回旧结构体会让前端看到"改成功了但时间没变"。
	return s.repo.Get(ctx, id)
}

// ListChunks 返回某个文档的分块。
//
// 先读文档再读分块（而不是直接查 chunks）：文档不存在与"文档存在但没有分块"
// 是两种不同结果，前者要 404，后者是合法的空列表。
func (s *Service) ListChunks(ctx context.Context, documentID int64, limit, offset int) ([]Chunk, int64, error) {
	if _, err := s.repo.Get(ctx, documentID); err != nil {
		return nil, 0, err
	}
	limit, offset = ClampPaging(limit, offset)
	chunks, err := s.repo.ListChunks(ctx, documentID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.CountChunks(ctx, documentID)
	if err != nil {
		return nil, 0, err
	}
	return chunks, total, nil
}

// Delete 删除文档：先删库，提交成功后再删原始文件。
//
// 顺序不能反。先删文件再删库，一旦删库失败，留下的是一条"记录在、原文没了"
// 的文档——它还能出现在列表里，但重索引永远失败；而先删库后删文件失败，
// 最坏结果只是一个没人引用的孤儿文件，不影响任何功能。
//
// 所以文件删除失败**不回滚、不返回错误**，只记一条 warn：此时用户看到的
// "文档已删除"是真的，回滚反而会让他以为没删掉。
func (s *Service) Delete(ctx context.Context, id int64) error {
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	if doc.StoragePath == nil {
		return nil
	}
	if err := s.store.Remove(ctx, *doc.StoragePath); err != nil {
		s.log.Warn("remove document file failed",
			zap.Int64("document_id", id),
			zap.String("storage_path", *doc.StoragePath),
			zap.Error(err),
		)
	}
	return nil
}

// StartReindex 受理一次重索引：置 indexing + 建任务，返回 job_id。
//
// 与导入不同的是这里不检查原文是否可得：文件在 MinIO 里，读一次要几毫秒到几百毫秒，
// 放在请求里等于把外部依赖的延迟加到接口上。原文缺失由 worker 报 ErrSourceMissing，
// 任务落 failed 并把原因写进文档，用户看到的是"重新切分失败：原文已丢失"——
// 比在请求里 409 少一点即时性，但信息一样清楚，且状态是持久化的。
//
// 同一文档已有未完成的重索引任务时返回 ErrReindexRunning（409）：
// 重复点击不该排两条队，那只会让同一份文档被两个 worker 同时替换分块。
func (s *Service) StartReindex(ctx context.Context, id int64) (*ImportResult, error) {
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	dedupeKey := fmt.Sprintf("document:%d:reindex", id)
	queued := &job.Job{
		Kind:        KindDocumentReindex,
		Status:      job.StatusQueued,
		DedupeKey:   &dedupeKey,
		Payload:     job.Payload{},
		MaxAttempts: defaultMaxAttempts,
		RunAfter:    time.Now().UTC(),
	}
	if err := s.repo.MarkIndexingWithJob(ctx, id, queued); err != nil {
		if errors.Is(err, ErrDuplicate) {
			return nil, ErrReindexRunning
		}
		return nil, err
	}
	s.notify(ctx, queued.ID)
	doc.Status = StatusIndexing
	return &ImportResult{Document: doc, JobID: queued.ID}, nil
}

// notify 尝试叫醒 worker，失败只记 warn。
//
// 不把错误返回给调用方是刻意的：任务已经落库，投递失败只意味着"晚一点执行"，
// 不是"这次操作失败"。让 HTTP 接口因为 MQ 抖动返回 500，反而会误导用户重传。
func (s *Service) notify(ctx context.Context, jobID int64) {
	if jobID == 0 || s.jobs == nil {
		return
	}
	if err := s.jobs.Notify(ctx, jobID); err != nil {
		s.log.Warn("notify job queue failed",
			zap.Int64("job_id", jobID), zap.Error(err))
	}
}

// ProcessReindex 是重索引任务的执行段：用当前的分块规则重新切分一份已有文档。
//
// 存在的意义是"规则会变"：切分粒度、标题识别、未来的语言相关处理都可能在
// 后续阶段调整，已经入库的文档不应该只能靠重新上传才能用上新规则
// （重新上传会被内容哈希的幂等挡住，根本不会重切）。
// 原文取自存储（文件）或 documents.content（笔记），不依赖客户端再传一次。
//
// 顺序是先算完、再一次事务替换：算的途中失败什么都不改，
// 替换的途中失败事务回滚，旧分块完整保留。
func (s *Service) ProcessReindex(ctx context.Context, id int64) error {
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	text, err := s.sourceText(ctx, doc)
	if err != nil {
		return err
	}
	chunks, err := buildChunks(doc.MIME, text)
	if err != nil {
		return err
	}
	// 与导入共用 FinishImport：换分块与置 ready 必须同进同退。
	return s.repo.FinishImport(ctx, id, chunks)
}

// CancelProcessing 请求取消某文档进行中的处理。
//
// 取消是"请求"而不是"立即生效"：任务在跑时只能置成 cancelling，等 worker 心跳
// 发现后自己中断——它可能正卡在一次解析里，只有它知道何时能安全停下。
// 文档状态的收尾分两种情况，取决于任务当时在什么状态：
//   - 排队中（直接 cancelled）→ 立刻写终态：没有任何东西还在跑；
//   - 运行中（cancelling）→ 只标"取消中"，终态由 worker 停下后回调写。
//     这里抢先写 failed 会与"处理其实刚好完成"互相覆盖——实测出现过
//     failed 但已有分块的半状态，所以收尾必须跟着任务状态的原子转移走。
//
// 终态本身取决于被取消的是哪种任务：
//   - 导入取消 → 这份文档没成，标 failed 并写明原因；
//   - 重索引取消 → 旧分块完好，退回 ready 才是事实，标失败会让人以为文档坏了。
func (s *Service) CancelProcessing(ctx context.Context, id int64) (*Document, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	_, kind, status, err := s.jobs.RequestCancel(ctx, id)
	if err != nil {
		if errors.Is(err, job.ErrNoJob) {
			// 没有进行中的任务：可能是已经完成、已经被取消，或本来就没在处理。
			// 三种情况对调用方是同一件事——没得取消。
			return nil, ErrNoProcessing
		}
		return nil, err
	}
	if status == job.StatusCancelled {
		// 排队中的任务已被直接取消，没有任何东西还在跑，可以立刻收尾。
		if err := s.SettleCancelled(ctx, id, kind == KindDocumentReindex); err != nil {
			return nil, err
		}
		return s.repo.Get(ctx, id)
	}

	// 运行中的任务只能置 cancelling：worker 可能正卡在一次解析里，
	// 它停下来之后会通过 OnCancelled 回调写终态。这里先如实标"取消中"，
	// 而不是抢先写 failed —— 那会与"处理其实已经完成了"互相覆盖。
	if err := s.repo.MarkCancelling(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
}

// SettleCancelled 写取消之后的业务终态。由 worker 在任务真正停下后回调，
// 也在"排队中直接取消"时被 CancelProcessing 调用。
//
// 重索引取消 → 退回 ready：旧分块完好，文档本身没问题；
// 导入取消 → failed + cancelled：这份文档确实没有内容可用。
func (s *Service) SettleCancelled(ctx context.Context, documentID int64, reindex bool) error {
	if reindex {
		return s.repo.MarkReady(ctx, documentID)
	}
	doc, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return err
	}
	if doc.ChunkCount > 0 {
		// 取消来晚了一步，内容其实已经写完：保留成果比标失败更诚实，
		// 否则会出现"失败但有分块"的半状态。这条也是取消与完成赛跑时的兜底。
		return s.repo.MarkReady(ctx, documentID)
	}
	return s.repo.MarkFailed(ctx, documentID, "cancelled", "处理已取消")
}

// sourceText 取出文档的原始正文，供重索引使用。
func (s *Service) sourceText(ctx context.Context, doc *Document) (string, error) {
	if doc.StoragePath != nil {
		data, err := s.store.Read(ctx, *doc.StoragePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("%w: file %s is gone", ErrSourceMissing, *doc.StoragePath)
			}
			// 其余读失败（连不上、超时）是暂时故障，单独标记以便重试与给出准确错误码。
			return "", fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
		}
		// 再走一次 normalizeText：原文可能带着 BOM，偏移必须以去 BOM 之后
		// 的字节为基准，否则重索引产出的偏移和导入时不一致。
		text, _, err := normalizeText(data)
		return text, err
	}
	if doc.Content != nil {
		text, _, err := normalizeText([]byte(*doc.Content))
		return text, err
	}
	return "", ErrSourceMissing
}

// 元信息边界。这些上限不是性能考虑，而是"让筛选有意义"：
// 几百个标签的分类法在界面上无法使用，也会让 ANY(tags) 的过滤结果失去区分度。
const (
	maxTagsPerDocument = 20
	maxTagRunes        = 32
	maxCollectionRunes = 64
)

// normalizeMetadata 校验并规整分类与标签，返回可直接入库的形态。
//
// 返回 *string 而不是 string：数据库里"未分类"是 NULL，
// 空字符串会让"未分类"出现两种表示，筛选时还要额外兼容。
func normalizeMetadata(collection string, tags []string) (*string, StringArray, error) {
	trimmed := strings.TrimSpace(collection)
	var collectionPtr *string
	if trimmed != "" {
		if utf8.RuneCountInString(trimmed) > maxCollectionRunes {
			return nil, nil, fmt.Errorf("%w: collection must be at most %d characters",
				ErrInvalidMetadata, maxCollectionRunes)
		}
		collectionPtr = &trimmed
	}
	normalized, err := normalizeTags(tags)
	if err != nil {
		return nil, nil, err
	}
	return collectionPtr, normalized, nil
}

// normalizeTags 去空白、丢空项、去重（保持输入顺序），并检查长度与数量。
//
// 不允许逗号：文件导入的标签是 `tags=a,b` 这种逗号分隔写法，
// 允许标签里带逗号就没有任何办法把它和分隔符区分开。
// 与其在解析上发明一套转义规则，不如直接拒绝——分类标签带逗号本来也很少见。
func normalizeTags(tags []string) (StringArray, error) {
	out := make(StringArray, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if strings.ContainsRune(tag, ',') {
			return nil, fmt.Errorf("%w: tag %q must not contain a comma", ErrInvalidMetadata, tag)
		}
		if utf8.RuneCountInString(tag) > maxTagRunes {
			return nil, fmt.Errorf("%w: tag %q is longer than %d characters",
				ErrInvalidMetadata, tag, maxTagRunes)
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) > maxTagsPerDocument {
		return nil, fmt.Errorf("%w: at most %d tags are allowed", ErrInvalidMetadata, maxTagsPerDocument)
	}
	return out, nil
}

// utf8BOM 是文件开头的字节序标记。Windows 记事本保存的 UTF-8 文件常带它。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// normalizeText 统一做三件事：去 BOM、校验编码、拒绝空内容。
//
// 返回值里的 data 是**去掉 BOM 之后**的字节：原始文件、内容哈希、正文偏移
// 三者都以它为基准，这样"偏移能切回原文"在重索引时依然成立。
func normalizeText(data []byte) (text string, cleaned []byte, err error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return "", nil, ErrNotUTF8
	}
	text = string(data)
	if strings.TrimSpace(text) == "" {
		return "", nil, ErrEmptyDocument
	}
	return text, data, nil
}

// buildChunks 是所有来源共用的"解析 + 切分"入口。
func buildChunks(mime, text string) ([]Chunk, error) {
	blocks, err := Parse(mime, text)
	if err != nil {
		return nil, err
	}
	chunks := Split(text, blocks, DefaultChunkOptions)
	if len(chunks) == 0 {
		return nil, ErrEmptyDocument
	}
	return chunks, nil
}

// readAllLimited 读取最多 limit 字节，超出返回 ErrTooLarge。
//
// 多读 1 字节是为了区分"恰好等于上限"与"超过上限"：只读 limit 的话，
// 超限文件会被静默截断成合法文档，那是比直接报错更糟的行为。
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// ClampPaging 把分页参数收进合法区间。
//
// 导出是因为 handler 需要回显"实际生效的 limit/offset"——请求 limit=1000 时
// 实际只返回 100 条，响应里还写 1000 会让前端算错总页数。
// 放在 service 而不是 handler：这是业务约束（一次最多看多少条），
// 换一个入口（worker、CLI）时也该生效。
func ClampPaging(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// titleFromFilename 用文件名（去扩展名）做标题。
func titleFromFilename(name string) string {
	base := filepath.Base(name)
	title := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	if title == "" {
		return "未命名文档"
	}
	return truncateRunes(title, 200)
}

// titleForNote 优先用用户填的标题；没填就用正文第一行。
//
// 用第一行而不是"The story begins..."这种默认值：列表里一眼能认出是哪条笔记，
// 比"未命名笔记 3"有用得多。
func titleForNote(title, content string) string {
	if t := strings.TrimSpace(title); t != "" {
		return truncateRunes(t, 200)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line != "" {
			return truncateRunes(line, 60)
		}
	}
	return "未命名笔记"
}

// truncateRunes 按码点截断，避免把中文或 emoji 切成半个字符。
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit])
}
