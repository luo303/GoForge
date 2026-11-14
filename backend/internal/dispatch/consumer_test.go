package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luo303/mindos/backend/internal/platform"
)

type fakeStore struct {
	consumedIDs []string
	markCount   int
	retries     []platform.Envelope
	retryWaits  []time.Duration
	seen        map[string]bool
}

func newFakeStore() *fakeStore { return &fakeStore{seen: map[string]bool{}} }

func (f *fakeStore) Consumed(_ context.Context, messageID string) (bool, error) {
	return f.seen[messageID], nil
}

func (f *fakeStore) MarkConsumed(_ context.Context, messageID, _ string) error {
	f.consumedIDs = append(f.consumedIDs, messageID)
	f.markCount++
	return nil
}

func (f *fakeStore) Retry(_ context.Context, env platform.Envelope, delay time.Duration, _ string) error {
	f.retries = append(f.retries, env)
	f.retryWaits = append(f.retryWaits, delay)
	return nil
}

func envOf(id, topic string, payload string) platform.Envelope {
	return platform.Envelope{MessageID: id, Topic: topic, MsgKey: "1", Payload: []byte(payload)}
}

func TestHandleRunsHandlerAndMarksConsumed(t *testing.T) {
	st := newFakeStore()
	c := NewConsumer(st, nil, 3)
	called := 0
	c.Register("document.import", func(context.Context, platform.Envelope) error {
		called++
		return nil
	})

	if err := c.Handle(context.Background(), envOf("m1", "document.import", `{"document_id":1}`)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if called != 1 || st.markCount != 1 {
		t.Fatalf("called=%d marked=%d, want 1/1", called, st.markCount)
	}
}

// 同一 message_id 重复投递时不能再执行一次：MQ 是 at-least-once。
func TestHandleSkipsDuplicatedMessage(t *testing.T) {
	st := newFakeStore()
	st.seen["m2"] = true
	c := NewConsumer(st, nil, 3)
	called := 0
	c.Register("document.import", func(context.Context, platform.Envelope) error {
		called++
		return nil
	})

	if err := c.Handle(context.Background(), envOf("m2", "document.import", `{}`)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if called != 0 {
		t.Error("duplicated message must not be executed again")
	}
}

// 失败但还有次数：写一条新的 outbox 消息，不是 nack 重投。
func TestHandleSchedulesRetryOnFailure(t *testing.T) {
	st := newFakeStore()
	c := NewConsumer(st, nil, 3)
	c.Register("document.import", func(context.Context, platform.Envelope) error {
		return errors.New("minio unavailable")
	})

	err := c.Handle(context.Background(), envOf("m3", "document.import", `{"document_id":1}`))
	if err == nil {
		t.Fatal("expected handler error to surface")
	}
	if len(st.retries) != 1 {
		t.Fatalf("retries = %d, want 1", len(st.retries))
	}
	if st.retryWaits[0] <= 0 {
		t.Errorf("retry delay = %s, want positive backoff", st.retryWaits[0])
	}
	if st.markCount != 0 {
		t.Error("message should not be marked consumed while a retry is pending")
	}
}

// 次数用尽：标成已消费并放弃，不能无限重投。
func TestHandleGivesUpAtMaxAttempts(t *testing.T) {
	st := newFakeStore()
	c := NewConsumer(st, nil, 3)
	c.Register("document.import", func(context.Context, platform.Envelope) error {
		return errors.New("bad content")
	})

	// 信封上 attempt=2，表示这已经是第三次尝试。
	env := envOf("m4", "document.import", `{"document_id":1}`)
	env.Attempt = 2
	if err := c.Handle(context.Background(), env); err == nil {
		t.Fatal("expected error")
	}
	if len(st.retries) != 0 {
		t.Errorf("retries = %d, want none after giving up", len(st.retries))
	}
	if st.markCount != 1 {
		t.Errorf("marked = %d, want 1 (dropped)", st.markCount)
	}
}

// 未知 topic 不能 panic，也不能被反复重投。
func TestHandleDropsUnknownTopic(t *testing.T) {
	st := newFakeStore()
	c := NewConsumer(st, nil, 3)

	err := c.Handle(context.Background(), envOf("m5", "unknown.topic", `{}`))
	if !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("error = %v, want ErrUnknownTopic", err)
	}
	if st.markCount != 1 || len(st.retries) != 0 {
		t.Errorf("marked=%d retries=%d, want 1/0", st.markCount, len(st.retries))
	}
}

func TestRetryBackoffGrowsAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 1; attempt <= 3; attempt++ {
		got := retryBackoff(attempt)
		if got <= prev {
			t.Fatalf("attempt %d = %s, want growth over %s", attempt, got, prev)
		}
		prev = got
	}
	if got := retryBackoff(20); got != 5*time.Minute {
		t.Errorf("cap = %s, want 5m", got)
	}
}
