package mycel

func (c *cache) getRemote(bucket, key string) (any, error) {
	c.nodeScoreHashMap.RLock()
	target := c.nodeScoreHashMap.data[bucket+key]
	c.nodeScoreHashMap.RUnlock()

	err := c.app.Send(nil, []string{target})
	if err != nil {
		return nil, err
	}
}
