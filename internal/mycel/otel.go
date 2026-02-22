package mycel

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	localityLocal  = "local"
	localityRemote = "remote"
)

func newMetrics(m metric.Meter) (*metrics, error) {
	gets, err := m.Int64Counter("mycorrizal.cache.gets",
		metric.WithDescription("Total get operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	sets, err := m.Int64Counter("mycorrizal.cache.sets",
		metric.WithDescription("Total set operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	deletes, err := m.Int64Counter("mycorrizal.cache.deletes",
		metric.WithDescription("Total delete operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	ttlUpdates, err := m.Int64Counter("mycorrizal.cache.ttl_updates",
		metric.WithDescription("Total SetTtl operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	hits, err := m.Int64Counter("mycorrizal.cache.hits",
		metric.WithDescription("Successful get lookups"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	misses, err := m.Int64Counter("mycorrizal.cache.misses",
		metric.WithDescription("Failed get lookups"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	lruEvictions, err := m.Int64Counter("mycorrizal.cache.lru_evictions",
		metric.WithDescription("Items dropped due to maxLen"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	ttlEvictions, err := m.Int64Counter("mycorrizal.cache.ttl_evictions",
		metric.WithDescription("Items expired by TTL worker"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	bucketSize, err := m.Int64UpDownCounter("mycorrizal.cache.bucket.size",
		metric.WithDescription("Current item count per bucket"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	duration, err := m.Int64Histogram("mycorrizal.cache.duration",
		metric.WithDescription("Operation latency by locality (local or remote)"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	errors, err := m.Int64Counter("mycorrizal.cache.errors",
		metric.WithDescription("Failed operations by locality (local or remote)"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, err
	}

	return &metrics{
		gets:         gets,
		sets:         sets,
		deletes:      deletes,
		ttlUpdates:   ttlUpdates,
		hits:         hits,
		misses:       misses,
		lruEvictions: lruEvictions,
		ttlEvictions: ttlEvictions,
		bucketSize:   bucketSize,
		duration:     duration,
		errors:       errors,
	}, nil
}

// trackGet records a get operation and returns a done func that records hit/miss and duration.
func (c *cache) trackGet(bucket, locality string) func(hit bool) {
	start := time.Now()
	attrs := metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("cache.locality", locality),
	)
	c.metrics.gets.Add(c.ctx, 1, attrs)
	return func(hit bool) {
		c.metrics.duration.Record(c.ctx, time.Since(start).Milliseconds(), attrs)
		if hit {
			c.metrics.hits.Add(c.ctx, 1, attrs)
		} else {
			c.metrics.misses.Add(c.ctx, 1, attrs)
		}
	}
}

// trackSet records a set operation and returns a done func that records duration and errors.
func (c *cache) trackSet(bucket, locality string) func(err error) {
	start := time.Now()
	attrs := metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("cache.locality", locality),
	)
	c.metrics.sets.Add(c.ctx, 1, attrs)
	return func(err error) {
		c.metrics.duration.Record(c.ctx, time.Since(start).Milliseconds(), attrs)
		if err != nil {
			c.metrics.errors.Add(c.ctx, 1, attrs)
		}
	}
}

// trackDelete records a delete operation and returns a done func that records duration and errors.
func (c *cache) trackDelete(bucket, locality string) func(err error) {
	start := time.Now()
	attrs := metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("cache.locality", locality),
	)
	c.metrics.deletes.Add(c.ctx, 1, attrs)
	return func(err error) {
		c.metrics.duration.Record(c.ctx, time.Since(start).Milliseconds(), attrs)
		if err != nil {
			c.metrics.errors.Add(c.ctx, 1, attrs)
		}
	}
}

// trackSetTtl records a ttl update operation and returns a done func that records duration and errors.
func (c *cache) trackSetTtl(bucket, locality string) func(err error) {
	start := time.Now()
	attrs := metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("cache.locality", locality),
	)
	c.metrics.ttlUpdates.Add(c.ctx, 1, attrs)
	return func(err error) {
		c.metrics.duration.Record(c.ctx, time.Since(start).Milliseconds(), attrs)
		if err != nil {
			c.metrics.errors.Add(c.ctx, 1, attrs)
		}
	}
}

// recordLruEviction records a single LRU eviction for the given bucket.
func (c *cache) recordLruEviction(ctx context.Context, bucket string) {
	c.metrics.lruEvictions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}

// recordTtlEviction records a single TTL eviction for the given bucket.
func (c *cache) recordTtlEviction(ctx context.Context, bucket string) {
	c.metrics.ttlEvictions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}

// recordBucketSize adjusts the bucket size gauge by delta (use +1 for add, -1 for remove).
func (c *cache) recordBucketSize(ctx context.Context, bucket string, delta int64) {
	c.metrics.bucketSize.Add(ctx, delta, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}
