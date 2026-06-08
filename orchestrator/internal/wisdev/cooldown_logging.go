package wisdev

import (
	"strings"
	"sync"
	"time"
)

const wisdevCooldownFallbackLogInterval = 15 * time.Second

var (
	wisdevCooldownFallbackLogMu sync.Mutex
	wisdevCooldownFallbackLogAt = map[string]time.Time{}
)

func shouldLogWisDevCooldownFallback(operation string, now time.Time) bool {
	key := strings.TrimSpace(operation)
	if key == "" {
		key = "unknown"
	}

	wisdevCooldownFallbackLogMu.Lock()
	defer wisdevCooldownFallbackLogMu.Unlock()

	if last, ok := wisdevCooldownFallbackLogAt[key]; ok && now.Sub(last) < wisdevCooldownFallbackLogInterval {
		return false
	}
	wisdevCooldownFallbackLogAt[key] = now
	return true
}

func resetWisDevCooldownFallbackLogForTest() {
	wisdevCooldownFallbackLogMu.Lock()
	defer wisdevCooldownFallbackLogMu.Unlock()
	wisdevCooldownFallbackLogAt = map[string]time.Time{}
}
