package job

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ErrNoJob 表示当前没有可领取的任务。
//
// 用一个哨兵错误而不是 (nil, nil)：调用方必须显式选择"没活干就等一会儿"还是
// "把 nil 当成功继续往下走"——后者会在 worker 里变成空指针 panic。
var ErrNoJob = errors.New("no claimable job")

// ErrLeaseLost 表示续约时发现任务已经不属于自己（租约过期后被别人接管，
// 或者被管理员取消）。worker 收到它必须立刻停止执行，不能继续写数据。
var ErrLeaseLost = errors.New("job lease lost")

// Repository 是 jobs 表的读写入口。
//
// 所有"状态迁移"都做成一条带 WHERE 条件的 UPDATE，而不是"先读再改"：
// 读和写之间隔着一次网络往返，另一个进程完全可能在这期间改了同一行。
// 条件更新把判断交给数据库，影响 0 行就说明前提已经不成立。
type Repository struct {
	db *gorm.DB
}

// NewRepository 创建任务仓储。
func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

// EnqueueInput 是入队参数。
type EnqueueInput struct {
	Kind        Kind
	Payload     Payload
	DedupeKey   *string
	MaxAttempts int
	// RunAfter 为零值时立即可以领取；退避重试时传未来的时间。
	RunAfter time.Time
}

// Enqueue 创建任务，返回 (任务, 是否新建)。
//
// 去重语义：同 kind + 同 dedupe_key 已经有一个 queued/running 的任务时，
// 不新建，返回既有的那条并标记 created=false。调用方据此告诉用户
// "已经在处理了"，而不是让他以为重复提交没生效。
//
// 实现刻意依赖唯一索引而不是先查后插：两个并发请求同时查不到、再同时插入，
// 是这类设计最典型的竞态。让数据库来判，冲突了再读回来。
func (r *Repository) Enqueue(ctx context.Context, in EnqueueInput) (*Job, bool, error) {
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	runAfter := in.RunAfter
	if runAfter.IsZero() {
		runAfter = time.Now().UTC()
	}

	// 最多两轮：第一轮撞唯一索引后读回既有任务；如果对手在这瞬间刚好完成
	// （读不到了），再插一次——此时名额已空，这次应该能成。
	for attempt := 0; attempt < 2; attempt++ {
		j := &Job{
			Kind:        in.Kind,
			Status:      StatusQueued,
			DedupeKey:   in.DedupeKey,
			Payload:     in.Payload,
			MaxAttempts: maxAttempts,
			RunAfter:    runAfter,
		}
		err := r.db.WithContext(ctx).Create(j).Error
		switch {
		case err == nil:
			return j, true, nil
		case !errors.Is(err, gorm.ErrDuplicatedKey):
			return nil, false, fmt.Errorf("create job: %w", err)
		case in.DedupeKey == nil:
			// 没有去重键却撞了唯一索引，说明约束与预期不一致，直接报错更好排查。
			return nil, false, fmt.Errorf("create job: unexpected duplicate without dedupe key: %w", err)
		}

		existing, findErr := r.findActive(ctx, in.Kind, *in.DedupeKey)
		if findErr == nil {
			return existing, false, nil
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil, false, findErr
		}
	}
	return nil, false, fmt.Errorf("create job: dedupe race on kind %s", in.Kind)
}

func (r *Repository) findActive(ctx context.Context, kind Kind, dedupeKey string) (*Job, error) {
	var found Job
	err := r.db.WithContext(ctx).
		Where("kind = ? AND dedupe_key = ? AND status IN ?", kind, dedupeKey,
			[]Status{StatusQueued, StatusRunning}).
		First(&found).Error
	if err != nil {
		return nil, err
	}
	return &found, nil
}

// Claim 领一条可执行的任务，没有则返回 ErrNoJob。
//
// SQL 里的三件事各自解决一个问题，缺一不可：
//   - FOR UPDATE SKIP LOCKED：并发领取时直接跳过别人锁住的行，不排队等待。
//     没有它，两个 worker 会互相阻塞，吞吐取决于运气。
//   - 外层 UPDATE ... WHERE id = (子查询)：判断与改状态在同一个原子操作里。
//     写成"先 SELECT 再 UPDATE"会留出一个窗口，同一条任务被领两次。
//   - attempts+1 在领取时加：失败次数统计的是"尝试"，不是"失败"。
//     进程被 kill 那次也算一次尝试，否则无限崩溃的任务会永远重试不完。
func (r *Repository) Claim(ctx context.Context, workerID string, lease time.Duration) (*Job, error) {
	const claimSQL = `
UPDATE jobs SET
    status           = 'running',
    worker_id        = ?,
    attempts         = attempts + 1,
    started_at       = COALESCE(started_at, now()),
    lease_expires_at = now() + make_interval(secs => ?),
    updated_at       = now()
WHERE id = (
    SELECT id FROM jobs
    WHERE status = 'queued' AND run_after <= now()
    ORDER BY run_after, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *`

	var claimed Job
	// 这条是全系统唯一高频执行的 SQL：空闲时每个 worker 每 500ms 查一次，
	// 开着 DB_LOG_SQL 会把控制台刷满，而 rows:0 本身就是"没活干"的正常含义，
	// 不是值得记录的事件。用 Silent 会话执行它，其余 SQL 照常记录。
	// 领取失败（真错误）仍会由调用方记 warn，不会因为静音而丢信息。
	if err := r.db.WithContext(ctx).
		Session(&gorm.Session{Logger: logger.Discard}).
		Raw(claimSQL, workerID, lease.Seconds()).Scan(&claimed).Error; err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if claimed.ID == 0 {
		// Scan 找不到行时不报错，只是一片零值。用 ID == 0 判定"没领到"。
		return nil, ErrNoJob
	}
	return &claimed, nil
}

// Heartbeat 续约并回报任务当前状态。
//
// 一次查询干两件事，因为这两件事必须在同一时刻成立：续约成功说明"任务还是我的"，
// 此时读到的状态才可信。分两次查会出现"刚续约成功、状态已经变了"的中间态。
//
// WHERE 里带 worker_id 是关键：租约过期被接管、或任务已被置成终态时，
// 这条 UPDATE 影响 0 行，返回 ErrLeaseLost —— 旧 worker 必须立刻停手，
// 否则两个进程会同时改同一个文档的分块。
//
// 状态范围包含 cancelling：处于取消中的任务仍然属于这个 worker，
// 它需要继续续约直到 handler 真的停下来，否则租约会先过期、任务被别人捡走。
func (r *Repository) Heartbeat(ctx context.Context, id int64, workerID string, lease time.Duration) (Status, error) {
	var status Status
	err := r.db.WithContext(ctx).Raw(`
UPDATE jobs SET lease_expires_at = now() + make_interval(secs => ?), updated_at = now()
WHERE id = ? AND worker_id = ? AND status IN ('running', 'cancelling')
RETURNING status`,
		lease.Seconds(), id, workerID).Scan(&status).Error
	if err != nil {
		return "", fmt.Errorf("heartbeat job %d: %w", id, err)
	}
	if status == "" {
		// Scan 找不到行时不报错，只留零值。
		return "", ErrLeaseLost
	}
	return status, nil
}

// Succeed 把任务标记为成功。
//
// WHERE 同样带 worker_id 与 status：如果执行期间租约已经丢了，这条更新不会生效，
// 调用方会收到 ErrLeaseLost —— 让旧 worker 知道"这活已经不算我的了"，
// 而不是让它心安理得地盖掉接管者的结果。
func (r *Repository) Succeed(ctx context.Context, id int64, workerID string) error {
	return r.finish(ctx, id, workerID, StatusSucceeded, nil, nil)
}

// Fail 把任务标记为失败并留下原因。
//
// code 给程序判断（Phase 6 后续据此决定可否重试），message 给人看；
// 两者分开存，是因为"贴给人看的错误文案"以后可能要多语言或改写，
// 而判断逻辑不能跟着文案变。
func (r *Repository) Fail(ctx context.Context, id int64, workerID, code, message string) error {
	return r.finish(ctx, id, workerID, StatusFailed, &code, &message)
}

func (r *Repository) finish(ctx context.Context, id int64, workerID string, status Status, code, message *string) error {
	result := r.db.WithContext(ctx).Exec(`
UPDATE jobs SET status = ?, last_error_code = ?, last_error = ?,
       finished_at = now(), lease_expires_at = NULL, updated_at = now()
WHERE id = ? AND worker_id = ? AND status = 'running'`,
		status, code, message, id, workerID)
	if result.Error != nil {
		return fmt.Errorf("finish job %d as %s: %w", id, status, result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrLeaseLost
	}
	return nil
}

// RetryLater 把失败的任务放回队列，并推迟到 delay 之后才可再次领取。
//
// 与 Release 的区别：Release 是"我们自己叫停"（attempts 减回、不需等待），
// 这里是"这次真的失败了"（attempts 保留，且要等退避时间）。
//
// last_error_code / last_error 在重试期间也写进去：排查"一直在重试但没人知道为什么"
// 时，这行就是全部线索；下一次失败会覆盖它。
func (r *Repository) RetryLater(ctx context.Context, id int64, workerID string, delay time.Duration, code, message string) error {
	result := r.db.WithContext(ctx).Exec(`
UPDATE jobs SET status = 'queued', worker_id = NULL, lease_expires_at = NULL,
       run_after = now() + make_interval(secs => ?),
       last_error_code = ?, last_error = ?, updated_at = now()
WHERE id = ? AND worker_id = ? AND status = 'running'`,
		delay.Seconds(), code, message, id, workerID)
	if result.Error != nil {
		return fmt.Errorf("retry job %d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Release 把任务放回队列，供 worker 优雅停止时使用。
//
// attempts 减回去是有意的：这次是"我们自己叫停"，不是任务失败。
// 不这么做的话，反复部署/重启会让任务白白消耗掉 max_attempts，
// 最终因为"重启太多次"被判失败——那不是用户或数据的问题。
//
// 副作用要求可重跑：任务可能已经改了一半（例如分块替换做到一半被取消），
// 所以每个 handler 必须按"从头再做一次也没问题"来实现。
func (r *Repository) Release(ctx context.Context, id int64, workerID string) error {
	result := r.db.WithContext(ctx).Exec(`
UPDATE jobs SET status = 'queued', worker_id = NULL, lease_expires_at = NULL,
       attempts = GREATEST(attempts - 1, 0), run_after = now(), updated_at = now()
WHERE id = ? AND worker_id = ? AND status = 'running'`, id, workerID)
	if result.Error != nil {
		return fmt.Errorf("release job %d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrLeaseLost
	}
	return nil
}

// RequeueExpiredLeases 把租约过期的 running 任务放回队列，返回处理条数。
//
// 为什么需要它：Claim 只挑 status = 'queued' 的任务。进程被 kill -9 之后，
// 手里那条任务停在 running、租约到期也不会自己变回 queued——没有补扫，
// 它就会永远 running（正是退出标准里点名要避免的状态）。
//
// 为什么不在 Claim 里顺手接管过期任务：
//   - 领取 SQL 会变复杂，而且"接管"这件事没有任何痕迹，排查时看不出发生过；
//   - 补扫可以独立跑、独立测、独立记日志（一次回收多少条、哪些 id）。
//
// grace 是在租约到期之后再多等的时间：机器时钟微小漂移、数据库与 worker 之间的
// 响应延迟都会让"刚好到期"的判断变得可疑，多等一会儿能避免把还活着的任务抢走。
//
// attempts 不回退：进程是在执行中死掉的，这次尝试确实发生了，
// 与优雅停止（Release，我们自己叫停）区别就在于此。
func (r *Repository) RequeueExpiredLeases(ctx context.Context, grace time.Duration) (int64, error) {
	// 与 Claim 同理：这是按间隔反复执行的维护语句，绝大多数时候回收 0 条。
	// 真正的"回收了多少"由 worker 在 n > 0 时以 info 记录，不需要 SQL 原文陪跑。
	result := r.db.WithContext(ctx).
		Session(&gorm.Session{Logger: logger.Discard}).
		Exec(`
UPDATE jobs SET status = 'queued', worker_id = NULL, lease_expires_at = NULL,
       run_after = now(), updated_at = now()
WHERE status = 'running' AND lease_expires_at IS NOT NULL
  AND lease_expires_at < now() - make_interval(secs => ?)`, grace.Seconds())
	if result.Error != nil {
		return 0, fmt.Errorf("requeue expired jobs: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// RequestCancel 为一个业务对象（例如某份文档）的未完成任务请求取消。
//
// 返回任务的 id、类型与"取消之后的状态"，调用方据此决定业务侧怎么收尾：
//   - queued → 直接 cancelled：还没开始跑，取消是即时的；
//   - running → cancelling：正在跑，只能请求。真正停下要等 worker 心跳发现，
//     因为它可能正卡在一次解析或一次模型调用里，只有它自己知道何时能安全中断。
//
// 按 payload 里的 document_id 找任务，而不是让调用方拼 dedupe_key：
// 导入任务的 key 用内容哈希（插入前就得成立），重索引用文档 id，
// 两套规则不该泄漏到业务层的取消逻辑里。jobs 表规模小，jsonb 全扫可以接受。
func (r *Repository) RequestCancel(ctx context.Context, documentID int64) (int64, Kind, Status, error) {
	var row struct {
		ID     int64
		Kind   Kind
		Status Status
	}
	// CASE 里重复判断 status 是为了让"直接取消"和"请求取消"在同一条语句完成，
	// 避免先查再改留下的窗口。
	err := r.db.WithContext(ctx).Raw(`
UPDATE jobs SET
    status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE 'cancelling' END,
    finished_at = CASE WHEN status = 'queued' THEN now() ELSE finished_at END,
    worker_id = CASE WHEN status = 'queued' THEN NULL ELSE worker_id END,
    lease_expires_at = CASE WHEN status = 'queued' THEN NULL ELSE lease_expires_at END,
    updated_at = now()
WHERE id = (
    SELECT id FROM jobs
    WHERE payload ->> 'document_id' = ? AND status IN ('queued', 'running')
    ORDER BY id DESC
    LIMIT 1
)
RETURNING id, kind, status`, strconv.FormatInt(documentID, 10)).Scan(&row).Error
	if err != nil {
		return 0, "", "", fmt.Errorf("request cancel for document %d: %w", documentID, err)
	}
	if row.ID == 0 {
		return 0, "", "", ErrNoJob
	}
	return row.ID, row.Kind, row.Status, nil
}

// Cancel 由 worker 在确认停下之后把任务写成终态 cancelled。
func (r *Repository) Cancel(ctx context.Context, id int64, workerID string) error {
	result := r.db.WithContext(ctx).Exec(`
UPDATE jobs SET status = 'cancelled', finished_at = now(), lease_expires_at = NULL, updated_at = now()
WHERE id = ? AND worker_id = ? AND status IN ('running', 'cancelling')`, id, workerID)
	if result.Error != nil {
		return fmt.Errorf("cancel job %d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Get 读一条任务，供接口与测试使用。
func (r *Repository) Get(ctx context.Context, id int64) (*Job, error) {
	var found Job
	if err := r.db.WithContext(ctx).First(&found, id).Error; err != nil {
		return nil, err
	}
	return &found, nil
}
