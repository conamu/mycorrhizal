package mycel

import (
	"time"
)

func (c *cache) getRemote(bucket, key string) (any, error) {
	target := c.getPrimaryNode(bucket, key)

	payload, err := c.gobEncode(remoteCachePayload{
		Operation: GET,
		Key:       key,
		Bucket:    bucket,
	})
	if err != nil {
		return nil, err
	}

	res, err := c.app.Request(payload, target, c.remoteTimeout)
	if err != nil {
		return nil, err
	}

	val, err := c.gobDecode(res)
	if err != nil {
		return nil, err
	}

	return val.Value, nil
}

func (c *cache) setRemote(bucket, key string, value any, ttl time.Duration) error {
	return c.setRemoteToNode(bucket, key, value, ttl, c.getPrimaryNode(bucket, key))
}

// setRemoteToNode sends a SET operation to an explicit target node.
// Used by the replication fan-out and the rebalancer.
func (c *cache) setRemoteToNode(bucket, key string, value any, ttl time.Duration, targetNodeId string) error {
	payload, err := c.gobEncode(remoteCachePayload{
		Operation: SET,
		Key:       key,
		Bucket:    bucket,
		Value:     value,
		Ttl:       ttl,
	})
	if err != nil {
		return err
	}

	res, err := c.app.Request(payload, targetNodeId, c.remoteTimeout)
	if err != nil {
		return err
	}
	c.logger.Debug(string(res))
	return nil
}

func (c *cache) deleteRemote(bucket, key string) error {
	return c.deleteRemoteFromNode(bucket, key, c.getPrimaryNode(bucket, key))
}

// deleteRemoteFromNode sends a DELETE operation to an explicit target node.
func (c *cache) deleteRemoteFromNode(bucket, key, targetNodeId string) error {
	payload, err := c.gobEncode(remoteCachePayload{
		Operation: DELETE,
		Key:       key,
		Bucket:    bucket,
	})
	if err != nil {
		return err
	}

	res, err := c.app.Request(payload, targetNodeId, c.remoteTimeout)
	if err != nil {
		return err
	}

	c.logger.Debug(string(res))
	return nil
}

func (c *cache) setTtlRemote(bucket, key string, ttl time.Duration) error {
	return c.setTtlRemoteToNode(bucket, key, ttl, c.getPrimaryNode(bucket, key))
}

// setTtlRemoteToNode sends a SETTTL operation to an explicit target node.
func (c *cache) setTtlRemoteToNode(bucket, key string, ttl time.Duration, targetNodeId string) error {
	payload, err := c.gobEncode(remoteCachePayload{
		Operation: SETTTL,
		Key:       key,
		Bucket:    bucket,
		Ttl:       ttl,
	})
	if err != nil {
		return err
	}

	res, err := c.app.Request(payload, targetNodeId, c.remoteTimeout)
	if err != nil {
		return err
	}
	c.logger.Debug(string(res))
	return nil
}
