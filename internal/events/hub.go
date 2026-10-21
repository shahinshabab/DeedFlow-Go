// Package events fans live progress messages out to browsers. Workers and web
// processes talk through Postgres LISTEN/NOTIFY (what Redis channel layers did
// in the Django project), so a worker running in another container reaches the
// websocket a user has open.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// Channel is the Postgres NOTIFY channel used.
const Channel = "deedflow_events"

type envelope struct {
	Group   string          `json:"g"`
	Payload json.RawMessage `json:"p"`
}

// Hub delivers published events to local subscribers.
type Hub struct {
	db   *db.DB
	log  *slog.Logger
	mu   sync.Mutex
	subs map[string]map[*Subscription]struct{}
}

// Subscription receives the raw JSON of events of one group.
type Subscription struct {
	C     chan []byte
	group string
	hub   *Hub
}

// NewHub makes a hub on db.
func NewHub(d *db.DB, log *slog.Logger) *Hub {
	return &Hub{db: d, log: log, subs: map[string]map[*Subscription]struct{}{}}
}

// Subscribe starts receiving events of group. Call Close when done.
func (h *Hub) Subscribe(group string) *Subscription {
	s := &Subscription{C: make(chan []byte, 64), group: group, hub: h}
	h.mu.Lock()
	if h.subs[group] == nil {
		h.subs[group] = map[*Subscription]struct{}{}
	}
	h.subs[group][s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Close stops the subscription.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	delete(s.hub.subs[s.group], s)
	if len(s.hub.subs[s.group]) == 0 {
		delete(s.hub.subs, s.group)
	}
	s.hub.mu.Unlock()
}

// Publish sends payload to every subscriber of group, in every process.
func (h *Hub) Publish(ctx context.Context, group string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	msg, err := json.Marshal(envelope{Group: group, Payload: raw})
	if err != nil {
		return err
	}
	if len(msg) > 7500 {
		// NOTIFY payloads are limited to 8000 bytes; progress texts are short,
		// but never fail a submission over an oversized one.
		payload["message"] = truncate(asString(payload["message"]), 1500)
		payload["error"] = truncate(asString(payload["error"]), 1500)
		raw, _ = json.Marshal(payload)
		msg, _ = json.Marshal(envelope{Group: group, Payload: raw})
	}
	return h.db.Notify(ctx, Channel, string(msg))
}

func asString(v any) string { s, _ := v.(string); return s }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (h *Hub) dispatch(group string, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[group] {
		select {
		case s.C <- payload:
		default: // a stuck browser must not block everyone else
		}
	}
}

// Run listens for notifications until ctx ends, reconnecting after failures.
func (h *Hub) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := h.listen(ctx); err != nil && ctx.Err() == nil {
			h.log.Warn("event listener lost; reconnecting", "err", err)
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
			}
		}
	}
}

func (h *Hub) listen(ctx context.Context) error {
	l, err := h.db.Listen(ctx, Channel)
	if err != nil {
		return err
	}
	defer l.Close()
	for {
		payload, err := l.Wait(ctx)
		if err != nil {
			return err
		}
		var env envelope
		if json.Unmarshal([]byte(payload), &env) == nil {
			h.dispatch(env.Group, env.Payload)
		}
	}
}
