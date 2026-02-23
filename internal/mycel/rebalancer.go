package mycel

import (
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/conamu/go-worker"
)

type rebalancer struct {
	cache       *cache
	wg          *sync.WaitGroup
	logger      *slog.Logger
	interval    time.Duration
	triggerChan chan struct{}
	w           *worker.Worker
}

func newRebalancer(c *cache, wg *sync.WaitGroup, logger *slog.Logger, interval time.Duration) *rebalancer {
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &rebalancer{
		cache:       c,
		wg:          wg,
		logger:      logger,
		interval:    interval,
		triggerChan: make(chan struct{}, 1),
	}
}

func (r *rebalancer) start() {
	r.w = worker.NewWorker(r.cache.ctx, "mycel-rebalancer", r.wg, r.rebalancerWorkerTask, r.logger, r.interval)
	go r.w.Start()

	// Trigger-listener: converts triggerChan signals into sends on the worker InputChan.
	// Uses the cache context so it shuts down with the rest of Mycel.
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			select {
			case <-r.cache.ctx.Done():
				return
			case <-r.triggerChan:
				// Non-blocking send: if the worker is currently executing, the trigger
				// is dropped — the rebalancer will run again on the next interval.
				select {
				case r.w.InputChan <- struct{}{}:
				default:
				}
			}
		}
	}()
}

// triggerImmediate requests a rebalancer run as soon as possible.
// Drops the trigger if one is already pending (coalescing).
func (r *rebalancer) triggerImmediate() {
	select {
	case r.triggerChan <- struct{}{}:
	default:
	}
}

func (r *rebalancer) stop() {
	if r.w != nil {
		r.w.Stop()
	}
}

func (r *rebalancer) rebalancerWorkerTask(w *worker.Worker, msg any) {
	// Jitter: spread rebalance load across the cluster when all nodes react to the
	// same topology event simultaneously (thundering herd prevention).
	jitter := time.Duration(rand.Int63n(int64(2 * time.Second)))
	time.Sleep(jitter)

	w.Logger.Info("rebalancer cycle starting")
	start := time.Now()

	keysProcessed := 0
	keysDeleted := 0
	replicationAttempts := 0
	replicationErrors := 0

	// Snapshot bucket names while holding a brief read lock.
	r.cache.lruBuckets.RLock()
	bucketNames := make([]string, 0, len(r.cache.lruBuckets.data))
	for name := range r.cache.lruBuckets.data {
		bucketNames = append(bucketNames, name)
	}
	r.cache.lruBuckets.RUnlock()

	type keySnapshot struct {
		key       string
		data      any
		expiresAt time.Time
	}

	for _, bucketName := range bucketNames {
		// Check for shutdown between buckets.
		if r.cache.ctx.Err() != nil {
			w.Logger.Info("rebalancer interrupted by context cancellation")
			return
		}

		r.cache.lruBuckets.RLock()
		bucket, exists := r.cache.lruBuckets.data[bucketName]
		r.cache.lruBuckets.RUnlock()
		if !exists {
			continue
		}

		// Snapshot all keys in this bucket under a brief read lock.
		// We release the lock before any network I/O.
		var keys []keySnapshot
		bucket.RLock()
		n := bucket.head
		for n != nil {
			n.RLock()
			keys = append(keys, keySnapshot{
				key:       n.key,
				data:      n.data,
				expiresAt: n.expiresAt,
			})
			n.RUnlock()
			n = n.next
		}
		bucket.RUnlock()

		for _, ks := range keys {
			if r.cache.ctx.Err() != nil {
				w.Logger.Info("rebalancer interrupted by context cancellation")
				return
			}

			fullKey := routingKey(bucketName, ks.key)
			replicas := r.cache.getReplicas(fullKey)
			limit := r.cache.replicas
			if len(replicas) < limit {
				limit = len(replicas)
			}
			if limit == 0 {
				continue
			}
			topReplicas := replicas[:limit]

			selfIsReplica := false
			for _, rep := range topReplicas {
				if rep.id == r.cache.nodeId {
					selfIsReplica = true
					break
				}
			}

			keysProcessed++

			if !selfIsReplica {
				// This node is no longer responsible for this key.
				w.Logger.Debug(fmt.Sprintf("rebalancer: evicting key %s/%s (no longer a replica)", bucketName, ks.key))
				if err := r.cache.deleteLocal(bucketName, ks.key); err != nil {
					w.Logger.Warn(fmt.Sprintf("rebalancer: failed to evict local key %s/%s: %v", bucketName, ks.key, err))
				}
				keysDeleted++
				continue
			}

			// This node is a replica. Propagate the key to the other replica nodes.
			ttl := time.Duration(0)
			if !ks.expiresAt.IsZero() {
				remaining := time.Until(ks.expiresAt)
				if remaining <= 0 {
					// Already expired; TTL eviction worker will clean this up.
					continue
				}
				ttl = remaining
			}

			for _, rep := range topReplicas {
				if rep.id == r.cache.nodeId {
					continue
				}
				replicationAttempts++
				if err := r.cache.setRemoteToNode(bucketName, ks.key, ks.data, ttl, rep.id); err != nil {
					replicationErrors++
					w.Logger.Warn(fmt.Sprintf("rebalancer: failed to replicate key %s/%s to node %s: %v",
						bucketName, ks.key, rep.id, err))
				}
			}
		}
	}

	w.Logger.Info("rebalancer cycle complete",
		slog.Duration("duration", time.Since(start)),
		slog.Int("keysProcessed", keysProcessed),
		slog.Int("keysDeleted", keysDeleted),
		slog.Int("replicationAttempts", replicationAttempts),
		slog.Int("replicationErrors", replicationErrors),
	)
}
