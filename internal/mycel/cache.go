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
	SetTtl(bucket, key string, ttl time.Duration) error
}

func (c *cache) CreateBucket(name string, ttl time.Duration, maxLen int) error {
	_, err := c.lruBuckets.CreateBucket(name, ttl, maxLen)
	if err != nil {
		return err
	}
	return nil
}

func (c *cache) Get(bucket, key string) (any, error) {
	locality := localityLocal
	if !c.isLocal(bucket, key) {
		locality = localityRemote
	}
	done := c.trackGet(bucket, locality)

	var val any
	var err error
	if locality == localityLocal {
		val, err = c.getLocal(bucket, key)
	} else {
		val, err = c.getRemote(bucket, key)
	}
	done(err == nil)
	return val, err
}

func (c *cache) Set(bucket, key string, value any, ttl time.Duration) error {
	locality := localityLocal
	if !c.isLocal(bucket, key) {
		locality = localityRemote
	}
	done := c.trackSet(bucket, locality)

	var err error
	if locality == localityLocal {
		c.logger.Debug(fmt.Sprintf("cache set local for key %s on bucket %s", key, bucket))
		err = c.setLocal(bucket, key, value, ttl)
	} else {
		c.logger.Debug(fmt.Sprintf("cache set remote for key %s on bucket %s", key, bucket))
		err = c.setRemote(bucket, key, value, ttl)
	}
	done(err)
	return err
}

/*TODO:
Delete could be optimized to utilize the KV map
to find the node and stitch the lru instead of finding the node through iteration
*/

func (c *cache) Delete(bucket, key string) error {
	locality := localityLocal
	if !c.isLocal(bucket, key) {
		locality = localityRemote
	}
	done := c.trackDelete(bucket, locality)

	var err error
	if locality == localityLocal {
		c.logger.Debug(fmt.Sprintf("cache delete local for key %s on bucket %s", key, bucket))
		err = c.deleteLocal(bucket, key)
	} else {
		c.logger.Debug(fmt.Sprintf("cache delete remote for key %s on bucket %s", key, bucket))
		err = c.deleteRemote(bucket, key)
	}
	done(err)
	return err
}

func (c *cache) SetTtl(bucket, key string, ttl time.Duration) error {
	locality := localityLocal
	if !c.isLocal(bucket, key) {
		locality = localityRemote
	}
	done := c.trackSetTtl(bucket, locality)

	var err error
	if locality == localityLocal {
		c.logger.Debug(fmt.Sprintf("cache set ttl for key %s on bucket %s", key, bucket))
		err = c.setTtlLocal(bucket, key, ttl)
	} else {
		c.logger.Debug(fmt.Sprintf("cache set ttl for key %s on bucket %s", key, bucket))
		err = c.setTtlRemote(bucket, key, ttl)
	}
	done(err)
	return err
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

func (c *cache) getReplicaNodes(bucket, key string) []string {
	return nil
}

// shutdown will gracefully mark the node as dead and sign off from the network
func (c *cache) shutdown() {

}
