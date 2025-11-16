package mycel

import (
	"fmt"
	"time"

	"github.com/conamu/go-worker"
)

func (c *cache) ttlEvictionWorkerTask(w *worker.Worker, msg any) {
	c.lruBuckets.RLock()
	defer c.lruBuckets.RUnlock()

	expiredKeys := make(map[string]string)

	for name, b := range c.lruBuckets.data {
		w.Logger.Debug(fmt.Sprintf("running eviction task for bucket %s", name))
		b.RLock()
		n := b.head
		for n != nil {
			n.RLock()
			next := n.next
			if !n.expiresAt.IsZero() {
				now := time.Now()
				if now.After(n.expiresAt) {
					expiredKeys[name] = n.key
				}
			}
			n.RUnlock()
			n = next
		}
		b.RUnlock()
	}

	for bucket, key := range expiredKeys {
		err := c.Delete(bucket, key)
		if err != nil {
			w.Logger.Error(fmt.Sprintf("error performing ttl evicition for key %s in bucket %s", key, bucket), err)
		}
	}

	w.Logger.Debug("eviction done")
}
