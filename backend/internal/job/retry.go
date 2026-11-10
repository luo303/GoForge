// 本文件集中"什么该重试、隔多久重试"这两个判断。
//
// 单独成文件而不是散在 worker 与业务包里的理由：这两件事一旦分开写，
// 很容易出现"worker 觉得该重试、业务已经把文档标成失败"的矛盾状态。
// 放在一处，调用方只有一种解释口径。
package job

import (
	"errors"
	"time"
)

// maxBackoff 是退避上限。任务等太久会让用户以为它消失了；
// 五分钟是一个折中：既给外部依赖（模型限流、数据库重启）留出恢复时间，
// 又不至于让人等到怀疑系统。
const maxBackoff = 5 * time.Minute

// BaseBackoff 返回第 attempts 次尝试失败后的等待时长（不含抖动）。
//
// 指数退避：3s → 6s → 12s → …，封顶 5 分钟。
// 从 3 秒起步而不是 1 秒，是因为绝大多数瞬时故障（连接被重置、
// 短暂限流）需要的就是几秒——太频繁的重试只会把对方打得更惨。
//
// 为什么单独暴露一个不带抖动的版本：抖动是随机的，测试无法断言；
// 把可测的部分（指数增长与封顶）与不可测的部分（随机）分开，
// worker 里再做抖动，测试就能稳定覆盖策略本身。
func BaseBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	// 移位而不是 math.Pow：整数运算没有浮点误差，也不需要类型转换。
	// attempts 超过 7 时 3s<<7 已经超过上限，循环里直接截断。
	delay := 3 * time.Second << (attempts - 1)
	if delay > maxBackoff || delay <= 0 { // <=0 防溢出成负数
		return maxBackoff
	}
	return delay
}

// Jitter 在基准时长上叠加 ±25% 的随机偏移，避免"同时失败的任务同时重试"。
//
// 场景很具体：模型限流时一批任务会一起失败，如果都按 3 秒后重试，
// 3 秒后又是一波同样大小的尖峰，对方继续限流，永远打不开。
func Jitter(base time.Duration, rnd func(int64) int64) time.Duration {
	if base <= 0 {
		return 0
	}
	span := int64(base) / 4 // ±25%
	if span <= 0 {
		return base
	}
	offset := rnd(2*span) - span
	jittered := base + time.Duration(offset)
	if jittered < 0 {
		return base
	}
	return jittered
}

// permanentError 标记"再试也不会成功"的错误。
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent 把错误标记为不可重试。
//
// 用包装而不是另造一套错误码：调用方仍然能用 errors.Is 判断原始哨兵
// （例如 knowledge.ErrUnsupportedType），同时 worker 能识别"别重试了"。
//
// 典型用法：文件类型不支持、内容不是 UTF-8、元信息超长——这些重试三次
// 得到的还是同一个结果，只会白占三次任务名额并延迟用户看到错误的时间。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent 判断错误是否被标记为不可重试。
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// ShouldRetry 决定一次失败之后要不要再排队。
//
// 两个条件缺一不可：错误本身可恢复，且尝试次数还没用尽。
// 判断放在这里而不是 worker 里，是因为业务层写"文档失败原因"时
// 也要用同一个结论，否则会出现任务还在重试、文档已经标成失败的情况。
func ShouldRetry(j *Job, err error) bool {
	if IsPermanent(err) {
		return false
	}
	max := j.MaxAttempts
	if max <= 0 {
		max = 3
	}
	return j.Attempts < max
}
