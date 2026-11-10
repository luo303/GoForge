package job

import (
	"errors"
	"testing"
	"time"
)

func TestBaseBackoffGrowsAndCaps(t *testing.T) {
	// 前几次必须严格变大：退避的意义就是"越失败等越久"。
	prev := time.Duration(0)
	for attempt := 1; attempt <= 5; attempt++ {
		got := BaseBackoff(attempt)
		if got <= prev {
			t.Fatalf("attempt %d = %s, want greater than previous %s", attempt, got, prev)
		}
		prev = got
	}
	if first := BaseBackoff(1); first != 3*time.Second {
		t.Errorf("first backoff = %s, want 3s", first)
	}
	// 上限：久到用户以为任务消失了，比多等一会儿更糟。
	// 3s<<7 = 6m24s 才越过 5 分钟，所以从第 8 次开始被截断。
	for _, attempt := range []int{8, 20, 100} {
		if got := BaseBackoff(attempt); got != maxBackoff {
			t.Errorf("attempt %d = %s, want cap %s", attempt, got, maxBackoff)
		}
	}
	// 0 与负数按第一次处理，不能让调用方因为算错下标拿到 0 等待时间。
	if got := BaseBackoff(0); got != BaseBackoff(1) {
		t.Errorf("attempt 0 = %s, want same as attempt 1", got)
	}
}

func TestJitterStaysWithinBounds(t *testing.T) {
	base := 10 * time.Second
	// 用固定的"伪随机"验证边界：最小偏移、最大偏移、中间值。
	cases := []struct {
		name string
		rnd  func(int64) int64
		want time.Duration
	}{
		{name: "下限", rnd: func(int64) int64 { return 0 }, want: base - base/4},
		{name: "上限", rnd: func(n int64) int64 { return n - 1 }, want: base + base/4 - 1},
		{name: "中间", rnd: func(n int64) int64 { return n / 2 }, want: base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Jitter(base, tc.rnd); got != tc.want {
				t.Errorf("Jitter = %s, want %s", got, tc.want)
			}
		})
	}
	if got := Jitter(0, func(int64) int64 { return 0 }); got != 0 {
		t.Errorf("Jitter(0) = %s, want 0", got)
	}
}

func TestShouldRetry(t *testing.T) {
	recoverable := errors.New("connection reset")
	permanent := Permanent(errors.New("unsupported file type"))

	cases := []struct {
		name     string
		job      Job
		err      error
		expected bool
	}{
		{name: "还有次数且可恢复", job: Job{Attempts: 1, MaxAttempts: 3}, err: recoverable, expected: true},
		{name: "次数用尽", job: Job{Attempts: 3, MaxAttempts: 3}, err: recoverable, expected: false},
		{name: "永久错误", job: Job{Attempts: 1, MaxAttempts: 3}, err: permanent, expected: false},
		{name: "未设上限时按默认三次", job: Job{Attempts: 1, MaxAttempts: 0}, err: recoverable, expected: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRetry(&tc.job, tc.err); got != tc.expected {
				t.Errorf("ShouldRetry = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestPermanentKeepsOriginalError(t *testing.T) {
	sentinel := errors.New("unsupported type")
	wrapped := Permanent(sentinel)

	// 包装之后仍能用 errors.Is 判断原始哨兵：业务层不必为了"可重试"改错误体系。
	if !errors.Is(wrapped, sentinel) {
		t.Error("Permanent should keep the original error reachable via errors.Is")
	}
	if !IsPermanent(wrapped) {
		t.Error("IsPermanent should recognize a wrapped error")
	}
	if IsPermanent(sentinel) {
		t.Error("IsPermanent should not flag an unwrapped error")
	}
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) should stay nil")
	}
}
