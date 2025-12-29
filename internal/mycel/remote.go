package mycel

import (
	"time"
)

func (c *cache) getRemote(bucket, key string) (any, error) {
	c.nodeScoreHashMap.RLock()
	target := c.nodeScoreHashMap.data[bucket+key]
	c.nodeScoreHashMap.RUnlock()

	payload, err := c.gobEncode(remoteCachePayload{
		Operation: GET,
		Key:       key,
		Bucket:    bucket,
	})
	if err != nil {
		return nil, err
	}

	res, err := c.app.Request(payload, target, time.Millisecond*100)
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

	res, err := c.app.Request(payload, c.nodeScoreHashMap.data[bucket+key], time.Millisecond*100)
	if err != nil {
		return err
	}
	c.logger.Debug(string(res))
	return nil
}
