package wisdev

import (
	"fmt"
	"strings"
	"sync"
)

var globalSteeringBus = newSteeringBus()

type steeringBus struct {
	mu       sync.RWMutex
	channels map[string]chan SteeringSignal
	pending  map[string][]SteeringSignal
	seen     map[string]struct{}
}

func newSteeringBus() *steeringBus {
	return &steeringBus{
		channels: make(map[string]chan SteeringSignal),
		pending:  make(map[string][]SteeringSignal),
		seen:     make(map[string]struct{}),
	}
}

func RegisterSteeringChannel(sessionID string) (<-chan SteeringSignal, func()) {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		return nil, func() {}
	}
	ch := make(chan SteeringSignal, 64)
	globalSteeringBus.mu.Lock()
	globalSteeringBus.channels[key] = ch
	pending := append([]SteeringSignal(nil), globalSteeringBus.pending[key]...)
	delete(globalSteeringBus.pending, key)
	globalSteeringBus.mu.Unlock()
	for _, signal := range pending {
		ch <- signal
	}
	return ch, func() {
		globalSteeringBus.mu.Lock()
		if current := globalSteeringBus.channels[key]; current == ch {
			delete(globalSteeringBus.channels, key)
			close(ch)
		}
		globalSteeringBus.mu.Unlock()
	}
}

func SubmitSteeringSignal(sessionID string, signal SteeringSignal) error {
	_, err := QueueSteeringSignal(sessionID, signal)
	return err
}

func QueueSteeringSignal(sessionID string, signal SteeringSignal) (bool, error) {
	delivered, _, err := queueSteeringSignal(sessionID, signal)
	return delivered, err
}

func queueSteeringSignal(sessionID string, signal SteeringSignal) (bool, bool, error) {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		return false, false, fmt.Errorf("session ID is required")
	}
	if signal.Timestamp == 0 {
		signal.Timestamp = NowMillis()
	}
	signalKey := steeringSignalKey(key, signal)
	globalSteeringBus.mu.Lock()
	defer globalSteeringBus.mu.Unlock()
	if _, exists := globalSteeringBus.seen[signalKey]; exists {
		return false, false, nil
	}
	globalSteeringBus.seen[signalKey] = struct{}{}
	ch := globalSteeringBus.channels[key]
	if ch != nil {
		select {
		case ch <- signal:
			return true, true, nil
		default:
			// Fall through to the pending queue so a saturated loop does not drop
			// operator steering.
		}
	}
	queue := append(globalSteeringBus.pending[key], signal)
	if len(queue) > 64 {
		queue = queue[len(queue)-64:]
	}
	globalSteeringBus.pending[key] = queue
	return false, true, nil
}

func ReplayJournaledSteeringSignals(sessionID string, journal *RuntimeJournal, limit int) int {
	key := strings.TrimSpace(sessionID)
	if key == "" || journal == nil {
		return 0
	}
	if limit <= 0 {
		limit = 64
	}
	replayed := 0
	entries := journal.ReadSession(key, limit)
	for _, entry := range entries {
		if entry.EventType != EventWisDevSteeringSignal {
			continue
		}
		signal, ok := steeringSignalFromJournalEntry(entry)
		if !ok {
			continue
		}
		if _, accepted, err := queueSteeringSignal(key, signal); err == nil && accepted {
			replayed++
		}
	}
	return replayed
}

func steeringSignalFromJournalEntry(entry RuntimeJournalEntry) (SteeringSignal, bool) {
	if entry.Payload == nil {
		return SteeringSignal{}, false
	}
	signalType := strings.TrimSpace(AsOptionalString(entry.Payload["type"]))
	if signalType == "" {
		return SteeringSignal{}, false
	}
	signal := SteeringSignal{
		Type:      signalType,
		Payload:   strings.TrimSpace(AsOptionalString(entry.Payload["payload"])),
		Queries:   stringSliceFromAny(entry.Payload["queries"]),
		Timestamp: int64FromAny(entry.Payload["timestamp"]),
	}
	if signal.Timestamp == 0 {
		signal.Timestamp = entry.CreatedAt
	}
	return signal, true
}

func int64FromAny(raw any) int64 {
	switch typed := raw.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return 0
		}
		var parsed int64
		if _, err := fmt.Sscan(value, &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

func steeringSignalKey(sessionID string, signal SteeringSignal) string {
	return strings.Join([]string{
		strings.TrimSpace(sessionID),
		strings.TrimSpace(signal.Type),
		strings.TrimSpace(signal.Payload),
		strings.Join(uniqueTrimmedStrings(signal.Queries), "\x1f"),
		fmt.Sprintf("%d", signal.Timestamp),
	}, "\x1e")
}

func stringSliceFromAny(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return uniqueTrimmedStrings(typed)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value := strings.TrimSpace(anyToString(item)); value != "" {
				out = append(out, value)
			}
		}
		return uniqueTrimmedStrings(out)
	}
	return nil
}
