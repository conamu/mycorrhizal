package mycel

import (
	"errors"
	"fmt"
	"time"
)

type Cache interface {
	CreateBucket(name string, ttl time.Duration, maxLen int) error
	Get(bucket, key string) (any, error)
	Put(bucket, key string, value any, ttl time.Duration) error
	Delete(bucket, key string) error
	PutTtl(bucket, key string, ttl time.Duration)
}

func (c *cache) CreateBucket(name string, ttl time.Duration, maxLen int) error {
	_, err := c.lruBuckets.CreateBucket(name, ttl, maxLen)
	if err != nil {
		return err
	}
	return nil
}

func (c *cache) Get(bucket, key string) (any, error) {
	// If in local kv cache the data is available locally
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

		return nil, errors.New(fmt.Sprintf("no bucket with name %s", bucket))
	}
	return nil, errors.New("not found")
}

func (c *cache) Put(bucket, key string, value any, ttl time.Duration) error {

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

/*TODO:
Delete could be optimized to utilize the KV map
to find the node and stitch the lru instead of finding the node through iteration
*/

func (c *cache) Delete(bucket, key string) error {
	b, err := c.lruBuckets.GetBucket(bucket)
	if err != nil {
		return err
	}
	b.Delete(key)
	c.keyVal.Delete(bucket + key)
	return nil
}

func (c *cache) PutTtl(bucket, key string, ttl time.Duration) {
	n := c.keyVal.Get(bucket + key)
	if n == nil {
		return
	}
	n.Lock()
	defer n.Unlock()
	n.expiresAt = time.Now().Add(ttl)
	c.keyVal.Set(bucket+key, n)
	return
}

// shutdown will gracefully mark the node as dead and sign off from the network
func (c *cache) shutdown() {

}
