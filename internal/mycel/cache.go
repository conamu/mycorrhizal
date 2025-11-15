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

func (c *cache) CreateBucket(name string, ttl time.Duration, maxLen int) error {
	c.lruBuckets.Lock()
	defer c.lruBuckets.Unlock()

	if c.lruBuckets.data[name] != nil {
		return errors.New("bucket already exists")
	}

	c.lruBuckets.data[name] = &lruBucket{
		maxLen: maxLen,
	}
	return nil
}

func (c *cache) Get(bucket, key string) (any, error) {
	// This means we have the data available locally
	if n := c.keyVal.Get(bucket + key); n != nil {
		n.RLock()
		defer n.RUnlock()

		if b := c.lruBuckets.GetBucket(bucket); b != nil {
			c.lruBuckets.RUnlock()
			b.Push(n)
			return n.data, nil
		}

		return nil, errors.New(fmt.Sprintf("no bucket with name %s", bucket))
	}
	return nil, errors.New("not found")
}

func (c *cache) Put(bucket, key string, value any, ttl time.Duration) error {
	//TODO implement me
	panic("implement me")
}

func (c *cache) Delete(bucket, key string) error {
	//TODO implement me
	panic("implement me")
}

func (c *cache) PutTtl(bucket, key string, ttl time.Duration) error {
	//TODO implement me
	panic("implement me")
}

// shutdown will gracefully mark the node as dead and sign off from the network
func (c *cache) shutdown() {

}
