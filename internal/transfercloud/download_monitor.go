package transfercloud

import (
	"sync"
	"time"
)

type downloadMonitor struct {
	mu                   sync.Mutex
	written, total       int64
	started, lastAdvance time.Time
	done, stopped        chan struct{}
}

func newDownloadMonitor(log func(int64, int64, time.Duration, time.Duration), interval time.Duration) *downloadMonitor {
	now := time.Now()
	m := &downloadMonitor{total: -1, started: now, lastAdvance: now, done: make(chan struct{}), stopped: make(chan struct{})}
	go func() {
		defer close(m.stopped)
		if log == nil {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.done:
				return
			case <-ticker.C:
				m.mu.Lock()
				now := time.Now()
				written, total, last := m.written, m.total, m.lastAdvance
				m.mu.Unlock()
				log(written, total, now.Sub(m.started), now.Sub(last))
			}
		}
	}()
	return m
}
func (m *downloadMonitor) update(written, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if written > m.written {
		m.written = written
		m.lastAdvance = time.Now()
	}
	if total > 0 {
		m.total = total
	}
}
func (m *downloadMonitor) stop() { close(m.done); <-m.stopped }
