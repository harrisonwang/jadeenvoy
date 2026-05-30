// Package event 是进程内事件 broker。
// publisher 写持久层 + 分发到订阅者；订阅者收到副本。
package event

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// Event 是 broker 流通的事件单元。
type Event struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	ThreadID    string          `json:"session_thread_id"`
	Type        string          `json:"type"`
	Seq         int64           `json:"seq"`
	Payload     json.RawMessage `json:"payload"`
	ProcessedAt int64           `json:"processed_at"`
}

// Broker 进程内 pub-sub。
type Broker struct {
	st *store.Store

	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{} // sessionID → set of channels

	// OnPublished 是 post-persist 钩子，broker 不感知具体下游（webhook 等）。
	// 调用方在 broker 之外注册，注意：这些回调在 Publish 调用同步 goroutine 里跑，
	// 内部应快速返回或起 goroutine。
	hookMu sync.RWMutex
	hooks  []func(ev Event)
}

func NewBroker(st *store.Store) *Broker {
	return &Broker{
		st:   st,
		subs: map[string]map[chan Event]struct{}{},
	}
}

// RegisterHook 注册 post-publish 回调。线程安全。
func (b *Broker) RegisterHook(fn func(ev Event)) {
	b.hookMu.Lock()
	defer b.hookMu.Unlock()
	b.hooks = append(b.hooks, fn)
}

// Publish 先持久化，然后分发给订阅者。
func (b *Broker) Publish(ctx context.Context, sessionID, evType, threadID string, payload json.RawMessage) (Event, error) {
	row, err := b.st.AppendEvent(ctx, store.AppendEventInput{
		SessionID: sessionID,
		ThreadID:  threadID,
		Type:      evType,
		Payload:   payload,
	})
	if err != nil {
		return Event{}, err
	}
	ev := Event{
		ID:        row.ID,
		SessionID: row.SessionID,
		ThreadID:  row.ThreadID,
		Type:      row.Type,
		Seq:       row.Seq,
		Payload:   row.Payload,
	}
	if row.ProcessedAt.Valid {
		ev.ProcessedAt = row.ProcessedAt.Int64
	}
	b.dispatch(ev)

	obs.SessionEvents.WithLabelValues(evType).Inc()

	// post-publish hooks（同步调用；处理方需自己 goroutine 化）
	b.hookMu.RLock()
	hooks := b.hooks
	b.hookMu.RUnlock()
	for _, h := range hooks {
		h(ev)
	}
	return ev, nil
}

func (b *Broker) dispatch(ev Event) {
	b.mu.RLock()
	subs := make([]chan Event, 0, len(b.subs[ev.SessionID]))
	for ch := range b.subs[ev.SessionID] {
		subs = append(subs, ch)
	}
	b.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// slow consumer 直接关
			b.mu.Lock()
			if _, ok := b.subs[ev.SessionID][ch]; ok {
				delete(b.subs[ev.SessionID], ch)
				close(ch)
			}
			b.mu.Unlock()
		}
	}
}

// Subscribe 订阅 session 后续事件。返回 ch（unbuffered close 表示 broker 主动断开）+ unsubscribe 函数。
func (b *Broker) Subscribe(sessionID string) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = map[chan Event]struct{}{}
	}
	b.subs[sessionID][ch] = struct{}{}
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		if _, ok := b.subs[sessionID][ch]; ok {
			delete(b.subs[sessionID], ch)
		}
		b.mu.Unlock()
	}
	return ch, unsub
}

// SubscriberCount 当前 session 的订阅者数量（测试用）。
func (b *Broker) SubscriberCount(sessionID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[sessionID])
}
