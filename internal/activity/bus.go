package activity

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Channel is one of the three Activity panes.
type Channel string

const (
	Inbound  Channel = "inbound"  // ACM/BMH → rf2vc (Redfish)
	Runtime  Channel = "runtime"  // housekeeping, scans, health
	Outbound Channel = "outbound" // rf2vc → vSphere mutations (or dry-run)
)

const defaultCap = 500

// Event is one log line shown in the Activity UI and mirrored to container logs.
type Event struct {
	ID      int64          `json:"id"`
	TS      time.Time      `json:"ts"`
	Channel Channel        `json:"channel"`
	Level   string         `json:"level"` // info | warn | error | dryrun
	Op      string         `json:"op"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
	DryRun  bool           `json:"dryRun,omitempty"`
}

// Bus is a process-wide ring buffer of activity events.
type Bus struct {
	mu   sync.RWMutex
	seq  atomic.Int64
	cap  int
	ring []Event
}

var defaultBus = New(defaultCap)

// Default returns the process-wide bus.
func Default() *Bus { return defaultBus }

// New creates a ring buffer with the given capacity.
func New(capacity int) *Bus {
	if capacity < 4 {
		capacity = 4
	}
	return &Bus{cap: capacity, ring: make([]Event, 0, capacity)}
}

// Emit appends an event, mirrors it to the standard logger, and returns it.
func (b *Bus) Emit(ch Channel, level, op, message string, detail map[string]any) Event {
	if level == "" {
		level = "info"
	}
	ev := Event{
		ID:      b.seq.Add(1),
		TS:      time.Now().UTC(),
		Channel: ch,
		Level:   level,
		Op:      op,
		Message: message,
		Detail:  detail,
		DryRun:  level == "dryrun",
	}
	b.mu.Lock()
	if len(b.ring) >= b.cap {
		copy(b.ring[0:], b.ring[1:])
		b.ring[b.cap-1] = ev
	} else {
		b.ring = append(b.ring, ev)
	}
	b.mu.Unlock()

	prefix := string(ch)
	if ev.DryRun {
		prefix += "/DRY-RUN"
	}
	if len(detail) > 0 {
		log.Printf("[%s] %s %s %v", prefix, op, message, detail)
	} else {
		log.Printf("[%s] %s %s", prefix, op, message)
	}
	return ev
}

// Since returns events with id > afterID (oldest first), optionally filtered by channel.
func (b *Bus) Since(afterID int64, ch Channel, limit int) []Event {
	if limit <= 0 || limit > b.cap {
		limit = b.cap
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Event, 0, limit)
	for _, ev := range b.ring {
		if ev.ID <= afterID {
			continue
		}
		if ch != "" && ev.Channel != ch {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Snapshot returns the newest events per channel (or all if ch empty).
func (b *Bus) Snapshot(ch Channel, limit int) []Event {
	if limit <= 0 {
		limit = 200
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	filtered := make([]Event, 0, len(b.ring))
	for _, ev := range b.ring {
		if ch != "" && ev.Channel != ch {
			continue
		}
		filtered = append(filtered, ev)
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	// copy
	out := make([]Event, len(filtered))
	copy(out, filtered)
	return out
}

// Helpers for call sites.
func In(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Inbound, "info", op, message, detail)
}
func InErr(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Inbound, "error", op, message, detail)
}
func Run(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Runtime, "info", op, message, detail)
}
func RunWarn(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Runtime, "warn", op, message, detail)
}
func RunErr(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Runtime, "error", op, message, detail)
}
func Out(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Outbound, "info", op, message, detail)
}
func OutDry(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Outbound, "dryrun", op, message, detail)
}
func OutErr(op, message string, detail map[string]any) Event {
	return defaultBus.Emit(Outbound, "error", op, message, detail)
}
