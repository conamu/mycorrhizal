package mycel

import (
	"errors"
	"fmt"
	"time"
)

var (
	ERR_NOT_FOUND = errors.New("not found")
	ERR_NO_BUCKET = errors.New("bucket not found")
)

type Cache interface {
	CreateBucket(name string, ttl time.Duration, maxLen int) error
	Get(bucket, key string) (any, error)
	Set(bucket, key string, value any, ttl time.Duration) error
	Delete(bucket, key string) error
	SetTtl(bucket, key string, ttl time.Duration)
}

func (c *cache) CreateBucket(name string, ttl time.Duration, maxLen int) error {
	_, err := c.lruBuckets.CreateBucket(name, ttl, maxLen)
	if err != nil {
		return err
	}
	return nil
}

func (c *cache) Get(bucket, key string) (any, error) {
	if c.isLocal(bucket, key) {
		return c.getLocal(bucket, key)
	}

	return c.getRemote(bucket, key)
}

func (c *cache) Set(bucket, key string, value any, ttl time.Duration) error {

	if c.isLocal(bucket, key) {
		c.logger.Debug(fmt.Sprintf("cache set local for key %s on bucket %s", key, bucket))
		return c.setLocal(bucket, key, value, ttl)
	}

	c.logger.Debug(fmt.Sprintf("cache set remote for key %s on bucket %s", key, bucket))
	return c.setRemote(bucket, key, value, ttl)
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

func (c *cache) SetTtl(bucket, key string, ttl time.Duration) {
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

func (c *cache) isLocal(bucket, key string) bool {
	primaryReplicaId := ""

	c.nodeScoreHashMap.RLock()
	if id, exists := c.nodeScoreHashMap.data[bucket+key]; exists {
		c.nodeScoreHashMap.RUnlock()
		primaryReplicaId = id
	} else {
		c.nodeScoreHashMap.RUnlock()
		// select primary calculated replica
		scoredNodes := c.getReplicas(bucket + key)
		primaryReplicaId = scoredNodes[0].id

		// store it for future reference
		c.nodeScoreHashMap.Lock()
		c.nodeScoreHashMap.data[bucket+key] = scoredNodes[0].id
		c.nodeScoreHashMap.Unlock()
	}

	if primaryReplicaId == c.nodeId {
		return true
	}
	return false
}

// shutdown will gracefully mark the node as dead and sign off from the network
func (c *cache) shutdown() {

}
