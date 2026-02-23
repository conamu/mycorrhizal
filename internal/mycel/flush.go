package mycel

import "github.com/conamu/go-worker"

// GeoFlushFunc is called by the flush worker on every interval.
// It receives a snapshot of all current geo entries for one bucket.
// The library does not import any database driver — the application
// wires in whatever persistence layer it needs.
type GeoFlushFunc func(bucket string, entries []GeoEntry) error

// flushWorkerTask is run by the geo flush worker on the configured interval.
// It iterates every registered geo store and calls the user-provided flush
// function with a point-in-time snapshot of current entries.
func (g *geoCache) flushWorkerTask(w *worker.Worker, _ any) {
	if g.flushFunc == nil {
		return
	}

	g.mu.RLock()
	buckets := make([]string, 0, len(g.stores))
	for name := range g.stores {
		buckets = append(buckets, name)
	}
	g.mu.RUnlock()

	for _, bucket := range buckets {
		g.mu.RLock()
		s, ok := g.stores[bucket]
		g.mu.RUnlock()
		if !ok {
			continue
		}

		s.RLock()
		entries := make([]GeoEntry, 0, len(s.points))
		for _, entry := range s.points {
			entries = append(entries, entry)
		}
		s.RUnlock()

		if len(entries) == 0 {
			continue
		}

		if err := g.flushFunc(bucket, entries); err != nil {
			w.Logger.Error("geo: flush failed", "bucket", bucket, "error", err)
		} else {
			w.Logger.Debug("geo: flush complete", "bucket", bucket, "entries", len(entries))
		}
	}
}
