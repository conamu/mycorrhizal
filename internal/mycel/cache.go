package mycel

import (
	"time"
)

type Cache interface {
	Get(bucket, key string) (any, error)
	Put(bucket, key string, value any, ttl time.Duration) error
	Delete(bucket, key string) error
	PutTtl(bucket, key string, ttl time.Duration) error
}

func (c *cache) Get(bucket, key string) (any, error) {

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
