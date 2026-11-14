// 本文件是任务系统对业务层的唯一入口。
//
// 业务模块（knowledge、以后的 memory/agent）只需要一个对象：既能唤醒任务，
// 又能取消任务。合成放在这里而不是让每个业务包各拿两三个依赖，
// 是为了让"业务依赖任务系统的哪几个能力"只有一处定义。
package job

import (
	"context"

	"go.uber.org/zap"
)

// Bus 把投递与取消合在一起。
type Bus struct {
	*Notifier
	repo *Repository
}

// NewBus 组装业务侧入口。queue 允许为 nil（broker 不可用时服务照常启动）。
func NewBus(repo *Repository, queue Queue, log *zap.Logger) *Bus {
	return &Bus{Notifier: NewNotifier(repo, queue, log), repo: repo}
}

// RequestCancel 转交给仓储，语义见 Repository.RequestCancel。
//
// 用一次转发而不是把 Repository 整个嵌进来：业务层不该顺手拿到
// Enqueue、Claim 这些它不该调的方法。
func (b *Bus) RequestCancel(ctx context.Context, documentID int64) (int64, Kind, Status, error) {
	return b.repo.RequestCancel(ctx, documentID)
}
