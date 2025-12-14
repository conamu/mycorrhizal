package mycel

import "time"

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
