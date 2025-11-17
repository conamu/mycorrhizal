package mycel

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/conamu/go-worker"
)

func (c *cache) ttlEvictionWorkerTask(w *worker.Worker, msg any) {
	c.lruBuckets.RLock()
	defer c.lruBuckets.RUnlock()

	expiredKeys := make(map[string][]string)

	for name, b := range c.lruBuckets.data {
		expiredKeys[name] = []string{}
		w.Logger.Debug(fmt.Sprintf("running eviction task for bucket %s", name))
		b.RLock()
		n := b.head
		for n != nil {
			n.RLock()
			next := n.next
			if !n.expiresAt.IsZero() {
				now := time.Now()
				if now.After(n.expiresAt) {
					expiredKeys[name] = append(expiredKeys[name], n.key)
				}
			}
			n.RUnlock()
			n = next
		}
		b.RUnlock()
	}

	for bucket, keys := range expiredKeys {
		for _, key := range keys {
			err := c.Delete(bucket, key)
			if err != nil {
				w.Logger.Error(fmt.Sprintf("error performing ttl evicition for key %s in bucket %s", key, bucket), err)
			}
		}
		w.Logger.Debug(fmt.Sprintf("ttl eviction task for bucket %s done, evicted %d keys", bucket, len(keys)))
	}

	w.Logger.Debug("eviction done")
}

func (c *cache) applicationReceiveTask(payload []byte) error {
	req := cacheRequest{}
	_, err := binary.Decode(payload, binary.LittleEndian, &req)
	if err != nil {

	}
	return nil
}
