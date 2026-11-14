// 本文件把"任务已入队"这件事投递到消息队列。
//
// 它是 jobs 表（outbox）与 broker 之间唯一的连接点，只有两个动作：
// 发一条消息、记下发过的时间。业务层不需要知道这两步的存在，
// 也不该因为投递失败而回滚事务——任务已经在表里，补投会兜住。
package job

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// Queue 是投递能力，由 platform.Rabbit 实现。
//
// 定义在消费方而不是 platform 里：job 只需要"发一条 job_id"，
// 不需要认识连接、交换机、ack 这些概念。
type Queue interface {
	Publish(ctx context.Context, jobID int64) error
}

// Notifier 负责"投递 + 记账"。
//
// 两件事必须成对出现：只发不记，补投会立刻重发一遍（重复投递，虽然无害但浪费）；
// 只记不发，任务要等到下一轮补投才被执行（延迟白白变大）。
type Notifier struct {
	repo  *Repository
	queue Queue
	log   *zap.Logger
}

// NewNotifier 组装。queue 允许为 nil：broker 不可用时服务照常启动，
// 此时 Notify 会明确报错，由补投接管。
func NewNotifier(repo *Repository, queue Queue, log *zap.Logger) *Notifier {
	if log == nil {
		log = zap.NewNop()
	}
	return &Notifier{repo: repo, queue: queue, log: log}
}

// Notify 投递一条任务。
//
// 返回的 error 只用于日志与决策参考，调用方**不要**回滚业务事务：
// 任务行已经在同一个事务里落库，投递失败退化成"延迟"，不是"丢数据"。
func (n *Notifier) Notify(ctx context.Context, jobID int64) error {
	if n == nil || n.queue == nil {
		return errors.New("job: queue is not available")
	}
	if err := n.queue.Publish(ctx, jobID); err != nil {
		return fmt.Errorf("publish job %d: %w", jobID, err)
	}
	if err := n.repo.MarkPublished(ctx, jobID); err != nil {
		// 消息已经发出去了，标记失败只会导致补投多喊一次。
		// 不返回错误：对调用方来说这次投递是成功的。
		n.log.Warn("mark job published failed", zap.Int64("job_id", jobID), zap.Error(err))
	}
	return nil
}
