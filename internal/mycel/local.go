package mycel

func (c *cache) getLocal(bucket, key string) (any, error) {
	if n := c.keyVal.Get(bucket + key); n != nil {
		b, err := c.lruBuckets.GetBucket(bucket)
		if err != nil {
			return nil, err
		}

		if b != nil {
			b.Push(n)
			n.RLock()
			defer n.RUnlock()
			return n.data, nil
		}

		return nil, ERR_NO_BUCKET
	}
	return nil, ERR_NOT_FOUND
}
