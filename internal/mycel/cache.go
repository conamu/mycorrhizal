package mycel

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

var (
	ERR_NOT_FOUND = errors.New("not found")
	ERR_NO_BUCKET = errors.New("bucket not found")
)

type Cache interface {
	CreateBucket(name string, ttl time.Duration, maxLen int) error
	// CreateGeoBucket creates a fully-replicated geo bucket for spatial queries.
	// precisions is the set of geohash precision levels to index (e.g. []uint{5, 9}).
	// Queries must specify one of the configured precisions.
	// The highest precision is used as the canonical stored geohash.
	CreateGeoBucket(name string, ttl time.Duration, maxLen int, precisions []uint) error
	Get(bucket, key string) (any, error)
	Set(bucket, key string, value any, ttl time.Duration) error
	Delete(bucket, key string) error
	SetTtl(bucket, key string, ttl time.Duration) error
	// Geo returns the GeoCache sub-interface for spatial location operations.
	Geo() GeoCache
}

func (c *cache) CreateBucket(name string, ttl time.Duration, maxLen int) error {
	_, err := c.lruBuckets.CreateBucket(name, ttl, maxLen)
	if err != nil {
		return err
	}
	return nil
}

func (c *cache) CreateGeoBucket(name string, ttl time.Duration, maxLen int, precisions []uint) error {
	if len(precisions) == 0 {
		return errors.New("at least one precision level is required")
	}
	_, err := c.lruBuckets.CreateGeoBucketInternal(name, ttl, maxLen, precisions)
	if err != nil {
		return err
	}
	// Register the bucket in the geo index store so queries can target it.
	if err := c.geo.createStore(name, precisions); err != nil {
		return err
	}
	return nil
}

func (c *cache) Geo() GeoCache {
	return c.geo
}

func (c *cache) Get(bucket, key string) (any, error) {
	// Try local first — O(1) keyVal map lookup. This hits for any node that
	// holds a replica of the key (primary or secondary), without consulting
	// isLocal() or making a network call.
	if val, err := c.getLocal(bucket, key); err == nil {
		done := c.trackGet(bucket, localityLocal)
		done(true)
		return val, nil
	}

	// Key not in local store. Route to the primary node.
	done := c.trackGet(bucket, localityRemote)
	val, err := c.getRemote(bucket, key)
	done(err == nil)
	return val, err
}

func (c *cache) Set(bucket, key string, value any, ttl time.Duration) error {
	locality := localityLocal
	if !c.isLocal(bucket, key) {
		locality = localityRemote
	}
	done := c.trackSet(bucket, locality)

	c.logger.Debug(fmt.Sprintf("cache set for key %s on bucket %s (locality: %s)", key, bucket, locality))
	err := c.setReplicated(bucket, key, value, ttl)
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

	c.logger.Debug(fmt.Sprintf("cache delete for key %s on bucket %s", key, bucket))
	err := c.deleteReplicated(bucket, key)
	done(err)
	return err
}

func (c *cache) SetTtl(bucket, key string, ttl time.Duration) error {
	locality := localityLocal
	if !c.isLocal(bucket, key) {
		locality = localityRemote
	}
	done := c.trackSetTtl(bucket, locality)

	c.logger.Debug(fmt.Sprintf("cache set ttl for key %s on bucket %s", key, bucket))
	err := c.setTtlReplicated(bucket, key, ttl)
	done(err)
	return err
}

// getPrimaryNode returns the primary replica node ID for a key, using nodeScoreHashMap as a cache.
// Uses getReplicas() only on cache miss.
func (c *cache) getPrimaryNode(bucket, key string) string {
	fullKey := routingKey(bucket, key)
	c.nodeScoreHashMap.RLock()
	if id, exists := c.nodeScoreHashMap.data[fullKey]; exists {
		c.nodeScoreHashMap.RUnlock()
		return id
	}
	c.nodeScoreHashMap.RUnlock()

	replicas := c.getReplicas(fullKey)
	if len(replicas) == 0 {
		return ""
	}
	primaryId := replicas[0].id

	c.nodeScoreHashMap.Lock()
	c.nodeScoreHashMap.data[fullKey] = primaryId
	c.nodeScoreHashMap.Unlock()
	return primaryId
}

// isLocal returns true if this node is among the top-N replicas for the given key.
// Uses replicaLocalityCache for O(1) lookups on the hot path; falls back to getReplicas() on miss.
func (c *cache) isLocal(bucket, key string) bool {
	fullKey := routingKey(bucket, key)

	c.replicaLocalityCache.RLock()
	if isLocal, exists := c.replicaLocalityCache.data[fullKey]; exists {
		c.replicaLocalityCache.RUnlock()
		return isLocal
	}
	c.replicaLocalityCache.RUnlock()

	replicas := c.getReplicas(fullKey)
	limit := c.replicas
	if len(replicas) < limit {
		limit = len(replicas)
	}
	selfIsReplica := false
	for i := 0; i < limit; i++ {
		if replicas[i].id == c.nodeId {
			selfIsReplica = true
			break
		}
	}

	c.replicaLocalityCache.Lock()
	c.replicaLocalityCache.data[fullKey] = selfIsReplica
	c.replicaLocalityCache.Unlock()
	return selfIsReplica
}

// invalidateCaches clears both the primary node cache and the replica locality cache.
// Must be called when cluster topology changes so stale routing decisions are discarded.
func (c *cache) invalidateCaches() {
	c.nodeScoreHashMap.Lock()
	c.nodeScoreHashMap.data = make(map[string]string)
	c.nodeScoreHashMap.Unlock()

	c.replicaLocalityCache.Lock()
	c.replicaLocalityCache.data = make(map[string]bool)
	c.replicaLocalityCache.Unlock()
}

// getReplicaNodes returns the top-N replica nodes for a key (N = c.replicas).
func (c *cache) getReplicaNodes(bucket, key string) []replicaNode {
	replicas := c.getReplicas(routingKey(bucket, key))
	limit := c.replicas
	if len(replicas) < limit {
		limit = len(replicas)
	}
	return replicas[:limit]
}

// setReplicated writes a key to all N replica nodes in parallel.
// Requires a quorum (majority) of successful writes before returning nil.
func (c *cache) setReplicated(bucket, key string, value any, ttl time.Duration) error {
	replicaSet := c.getReplicaNodes(bucket, key)
	if len(replicaSet) == 0 {
		return fmt.Errorf("no replicas found for %s/%s", bucket, key)
	}
	results := make(chan error, len(replicaSet))

	for _, rep := range replicaSet {
		r := rep
		go func() {
			if r.id == c.nodeId {
				results <- c.setLocal(bucket, key, value, ttl)
			} else {
				results <- c.setRemoteToNode(bucket, key, value, ttl, r.id)
			}
		}()
	}

	successCount := 0
	var lastErr error
	for range replicaSet {
		if err := <-results; err == nil {
			successCount++
		} else {
			c.logger.Warn(fmt.Sprintf("replica write failed for key %s/%s: %v", bucket, key, err))
			lastErr = err
		}
	}

	quorum := (len(replicaSet) / 2) + 1
	if successCount < quorum {
		return fmt.Errorf("write failed quorum %d/%d: %w", successCount, len(replicaSet), lastErr)
	}
	return nil
}

// deleteReplicated deletes a key from all N replica nodes in parallel.
// ERR_NOT_FOUND on a replica counts as success (idempotent delete).
func (c *cache) deleteReplicated(bucket, key string) error {
	replicaSet := c.getReplicaNodes(bucket, key)
	if len(replicaSet) == 0 {
		return fmt.Errorf("no replicas found for %s/%s", bucket, key)
	}
	results := make(chan error, len(replicaSet))

	for _, rep := range replicaSet {
		r := rep
		go func() {
			var err error
			if r.id == c.nodeId {
				err = c.deleteLocal(bucket, key)
			} else {
				err = c.deleteRemoteFromNode(bucket, key, r.id)
			}
			// Not-found is success for idempotent delete
			if errors.Is(err, ERR_NOT_FOUND) {
				err = nil
			}
			results <- err
		}()
	}

	successCount := 0
	var lastErr error
	for range replicaSet {
		if err := <-results; err == nil {
			successCount++
		} else {
			c.logger.Warn(fmt.Sprintf("replica delete failed for key %s/%s: %v", bucket, key, err))
			lastErr = err
		}
	}

	quorum := (len(replicaSet) / 2) + 1
	if successCount < quorum {
		return fmt.Errorf("delete failed quorum %d/%d: %w", successCount, len(replicaSet), lastErr)
	}
	return nil
}

// setTtlReplicated updates the TTL for a key on all N replica nodes in parallel.
func (c *cache) setTtlReplicated(bucket, key string, ttl time.Duration) error {
	replicaSet := c.getReplicaNodes(bucket, key)
	if len(replicaSet) == 0 {
		return fmt.Errorf("no replicas found for %s/%s", bucket, key)
	}
	results := make(chan error, len(replicaSet))

	for _, rep := range replicaSet {
		r := rep
		go func() {
			if r.id == c.nodeId {
				results <- c.setTtlLocal(bucket, key, ttl)
			} else {
				results <- c.setTtlRemoteToNode(bucket, key, ttl, r.id)
			}
		}()
	}

	successCount := 0
	var lastErr error
	for range replicaSet {
		if err := <-results; err == nil {
			successCount++
		} else {
			c.logger.Warn(fmt.Sprintf("replica setttl failed for key %s/%s: %v", bucket, key, err))
			lastErr = err
		}
	}

	quorum := (len(replicaSet) / 2) + 1
	if successCount < quorum {
		return fmt.Errorf("setttl failed quorum %d/%d: %w", successCount, len(replicaSet), lastErr)
	}
	return nil
}

// routingKey builds an unambiguous composite key from a bucket name and item key,
// using a length prefix to prevent collisions (e.g. bucket "ab"+key "c" vs bucket "a"+key "bc").
func routingKey(bucket, key string) string {
	return strconv.Itoa(len(bucket)) + ":" + bucket + key
}

// shutdown will gracefully mark the node as dead and sign off from the network
func (c *cache) shutdown() {

}
