package mycel

import (
	"errors"
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

func (c *cache) applicationReceiveFunc(payload []byte) error {
	c.logger.Warn("application receive function not set")
	return nil
}

func (c *cache) applicationRequestHandlerFunc(payload []byte, senderId string) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty cache request payload")
	}

	switch payload[0] {
	case GET:
		c.logger.Debug(fmt.Sprintf("received GET request from %s", senderId))
	case SET:
		c.logger.Debug(fmt.Sprintf("received SET request from %s", senderId))
	case DELETE:
		c.logger.Debug(fmt.Sprintf("received DELETE request from %s", senderId))
	}

	return nil, nil
}
