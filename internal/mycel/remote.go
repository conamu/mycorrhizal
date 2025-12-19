package mycel

import (
	"time"
)

func (c *cache) getRemote(bucket, key string) ([]byte, error) {
	c.nodeScoreHashMap.RLock()
	target := c.nodeScoreHashMap.data[bucket+key]
	c.nodeScoreHashMap.RUnlock()

	res, err := c.app.Request(nil, target, time.Millisecond*100)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (c *cache) setRemote(bucket, key string, value any, ttl time.Duration) error {

	buf := make([]byte, 1)

	buf[0] = SET

	res, err := c.app.Request(buf, c.nodeScoreHashMap.data[bucket+key], ttl)
	if err != nil {
		return err
	}
	c.logger.Debug(string(res))
	return nil
}
