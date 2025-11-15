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
	PutTtl(bucket, key string, ttl time.Duration) error
}

func (c *cache) CreateBucket(name string, ttl time.Duration, maxLen int) (*lruBucket, error) {
	return c.lruBuckets.CreateBucket(name, ttl, maxLen)
}

func (c *cache) Get(bucket, key string) (any, error) {
	// This means we have the data available locally
	if n := c.keyVal.Get(bucket + key); n != nil {
		n.RLock()
		defer n.RUnlock()

		b, err := c.lruBuckets.GetBucket(bucket)
		if err != nil {
			return nil, err
		}

		if b != nil {
			c.lruBuckets.RUnlock()
			b.Push(n)
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

	n := &node{
		key:  bucket + key,
		data: value,
		ttl:  expiry,
	}

	c.keyVal.Set(bucket+key, n)
	b.Push(n)
	return nil
}

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
	n.Lock()
	defer n.Unlock()
	n.ttl = time.Now().Add(ttl)
	c.keyVal.Set(bucket+key, n)
	return
}

// shutdown will gracefully mark the node as dead and sign off from the network
func (c *cache) shutdown() {

}
