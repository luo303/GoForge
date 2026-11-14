package outbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStore 记录 relay 对外的每个动作，便于断言"该发还是该退避"。
type fakeStore struct {
	pending   []Message
	sent      []int64
	resched   []int64
	reschedIn []time.Duration
	givenUp   []int64
}

func (f *fakeStore) Pending(_ context.Context, limit int) ([]Message, error) {
	if len(f.pending) > limit {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}

func (f *fakeStore) MarkSent(_ context.Context, id int64) error {
	f.sent = append(f.sent, id)
	return nil
}

func (f *fakeStore) Reschedule(_ context.Context, id int64, delay time.Duration, _ string) error {
	f.resched = append(f.resched, id)
	f.reschedIn = append(f.reschedIn, delay)
	return nil
}

func (f *fakeStore) GiveUp(_ context.Context, id int64, _ string) error {
	f.givenUp = append(f.givenUp, id)
	return nil
}

type fakeQueue struct {
	err   error
	sent  []Message
	fails int // 前 N 次调用失败
	calls int
}

func (f *fakeQueue) Publish(_ context.Context, m Message) error {
	f.calls++
	if f.fails > 0 && f.calls <= f.fails {
		return errors.New("broker unavailable")
	}
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func TestRelayMarksSentOnSuccess(t *testing.T) {
	st := &fakeStore{pending: []Message{{ID: 1, Topic: "document.import", MsgKey: "1"}}}
	q := &fakeQueue{}
	r := NewRelay(st, q, nil, RelayOptions{})

	sent, err := r.Drain(context.Background())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if sent != 1 || len(st.sent) != 1 || st.sent[0] != 1 {
		t.Fatalf("sent=%d store.sent=%v, want 1 message marked sent", sent, st.sent)
	}
	if len(st.resched) != 0 || len(st.givenUp) != 0 {
		t.Errorf("unexpected retry actions: resched=%v giveup=%v", st.resched, st.givenUp)
	}
}

func TestRelayReschedulesWithBackoffOnFailure(t *testing.T) {
	st := &fakeStore{pending: []Message{{ID: 2, Topic: "document.import", Attempts: 0}}}
	q := &fakeQueue{fails: 1}
	r := NewRelay(st, q, nil, RelayOptions{MaxAttempts: 3})

	if _, err := r.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(st.resched) != 1 || st.resched[0] != 2 {
		t.Fatalf("rescheduled = %v, want [2]", st.resched)
	}
	if st.reschedIn[0] <= 0 {
		t.Errorf("delay = %s, want positive backoff", st.reschedIn[0])
	}
	if len(st.givenUp) != 0 {
		t.Errorf("give up = %v, want none while attempts remain", st.givenUp)
	}
}

func TestRelayGivesUpAtMaxAttempts(t *testing.T) {
	st := &fakeStore{pending: []Message{{ID: 3, Topic: "document.import", Attempts: 2}}}
	q := &fakeQueue{fails: 1}
	r := NewRelay(st, q, nil, RelayOptions{MaxAttempts: 3})

	if _, err := r.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(st.givenUp) != 1 || st.givenUp[0] != 3 {
		t.Fatalf("given up = %v, want [3]", st.givenUp)
	}
	if len(st.resched) != 0 {
		t.Errorf("rescheduled = %v, want none after giving up", st.resched)
	}
}

// 一条消息投递失败不该拖垮同批其它消息：否则毒消息会把整条流水线卡住。
func TestRelayContinuesAfterOneFailure(t *testing.T) {
	st := &fakeStore{pending: []Message{
		{ID: 4, Topic: "document.import"},
		{ID: 5, Topic: "document.import"},
	}}
	q := &fakeQueue{fails: 1}
	r := NewRelay(st, q, nil, RelayOptions{MaxAttempts: 5})

	if _, err := r.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(st.sent) != 1 || st.sent[0] != 5 {
		t.Fatalf("sent = %v, want the second message delivered", st.sent)
	}
	if len(st.resched) != 1 || st.resched[0] != 4 {
		t.Fatalf("rescheduled = %v, want the first message retried", st.resched)
	}
}

func TestRelayBackoffGrowsAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 1; attempt <= 3; attempt++ {
		got := relayBackoff(attempt)
		if got <= prev {
			t.Fatalf("attempt %d = %s, want growth over %s", attempt, got, prev)
		}
		prev = got
	}
	if got := relayBackoff(50); got != maxRelayBackoff {
		t.Errorf("cap = %s, want %s", got, maxRelayBackoff)
	}
}

// queue 为 nil（broker 不可用）时 relay 必须静默退出，不能 panic。
func TestRelayIsNoopWithoutQueue(t *testing.T) {
	r := NewRelay(&fakeStore{}, nil, nil, RelayOptions{})
	sent, err := r.Drain(context.Background())
	if err != nil || sent != 0 {
		t.Fatalf("drain = %d, %v; want 0, nil", sent, err)
	}
}
