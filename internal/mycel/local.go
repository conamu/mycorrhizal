package mycel

import "time"

func (c *cache) getLocal(bucket, key string) (any, error) {
	if n := c.keyVal.Get(bucket + key); n != nil {
		b, err := c.lruBuckets.GetBucket(bucket)
		if err != nil {
			return nil, err
		}

		if b != nil {
			b.Push(n)
			n.RLock()
			defer n.RUnlock()
			return n.data, nil
		}

		return nil, ERR_NO_BUCKET
	}
	return nil, ERR_NOT_FOUND
}

func (c *cache) setLocal(bucket, key string, value any, ttl time.Duration) error {
	b, err := c.lruBuckets.GetBucket(bucket)
	if err != nil {
		return err
	}

	expiry := time.Time{}

	if ttl != 0 {
		expiry = time.Now().Add(ttl)
	}

	// Check if key already exists - update instead of creating new node
	if existingNode := c.keyVal.Get(bucket + key); existingNode != nil {
		existingNode.Lock()
		existingNode.data = value
		existingNode.expiresAt = expiry
		existingNode.Unlock()
		b.Push(existingNode)
		return nil
	}

	// Create new node for new key
	n := &node{
		key:       key,
		data:      value,
		expiresAt: expiry,
	}

	c.keyVal.Set(bucket+key, n)
	b.Push(n)

	// LRU Eviction
	if b.len > b.maxLen {
		b.Lock()
		defer b.Unlock()
		evictedNode := b.tail
		evictedNode.Lock()
		defer evictedNode.Unlock()

		b.tail = evictedNode.prev
		evictedNode.next = nil
		evictedNode.prev = nil
		evictedNode.data = nil
		c.keyVal.Delete(evictedNode.key)
		b.len--
	}

	return nil

}

func (c *cache) deleteLocal(bucket, key string) error {
	b, err := c.lruBuckets.GetBucket(bucket)
	if err != nil {
		return err
	}
	b.Delete(key)
	c.keyVal.Delete(bucket + key)
	return nil
}
