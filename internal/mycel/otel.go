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
	// ── Cache operations ──────────────────────────────────────────────────────

	gets, err := m.Int64Counter("mycorrhizal.cache.gets",
		metric.WithDescription("Total get operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	sets, err := m.Int64Counter("mycorrhizal.cache.sets",
		metric.WithDescription("Total set operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	deletes, err := m.Int64Counter("mycorrhizal.cache.deletes",
		metric.WithDescription("Total delete operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	ttlUpdates, err := m.Int64Counter("mycorrhizal.cache.ttl_updates",
		metric.WithDescription("Total SetTtl operations"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	hits, err := m.Int64Counter("mycorrhizal.cache.hits",
		metric.WithDescription("Successful get lookups"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	misses, err := m.Int64Counter("mycorrhizal.cache.misses",
		metric.WithDescription("Failed get lookups"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	lruEvictions, err := m.Int64Counter("mycorrhizal.cache.lru_evictions",
		metric.WithDescription("Items dropped due to maxLen"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	ttlEvictions, err := m.Int64Counter("mycorrhizal.cache.ttl_evictions",
		metric.WithDescription("Items expired by TTL worker"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	bucketSize, err := m.Int64UpDownCounter("mycorrhizal.cache.bucket.size",
		metric.WithDescription("Current item count per bucket"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	duration, err := m.Int64Histogram("mycorrhizal.cache.duration",
		metric.WithDescription("Operation latency by locality (local or remote)"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	errors, err := m.Int64Counter("mycorrhizal.cache.errors",
		metric.WithDescription("Failed operations by locality (local or remote)"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, err
	}

	// ── Distributed write path ────────────────────────────────────────────────

	replicaWrites, err := m.Int64Counter("mycorrhizal.cache.replica.writes",
		metric.WithDescription("Individual replica write outcomes during replicated set/delete"),
		metric.WithUnit("{write}"),
	)
	if err != nil {
		return nil, err
	}

	quorumFailures, err := m.Int64Counter("mycorrhizal.cache.quorum.failures",
		metric.WithDescription("Replicated writes or deletes that failed to reach quorum"),
		metric.WithUnit("{failure}"),
	)
	if err != nil {
		return nil, err
	}

	// ── Rebalancer ────────────────────────────────────────────────────────────

	rebalancerCycles, err := m.Int64Counter("mycorrhizal.rebalancer.cycles",
		metric.WithDescription("Number of completed rebalancer cycles"),
		metric.WithUnit("{cycle}"),
	)
	if err != nil {
		return nil, err
	}

	rebalancerKeysProcessed, err := m.Int64Counter("mycorrhizal.rebalancer.keys.processed",
		metric.WithDescription("Total keys evaluated across all rebalancer cycles"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	rebalancerKeysEvicted, err := m.Int64Counter("mycorrhizal.rebalancer.keys.evicted",
		metric.WithDescription("Keys deleted locally because this node is no longer a replica"),
		metric.WithUnit("{item}"),
	)
	if err != nil {
		return nil, err
	}

	rebalancerReplicationAttempts, err := m.Int64Counter("mycorrhizal.rebalancer.replication.attempts",
		metric.WithDescription("Outbound key replications attempted during a rebalancer cycle"),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, err
	}

	rebalancerReplicationErrors, err := m.Int64Counter("mycorrhizal.rebalancer.replication.errors",
		metric.WithDescription("Outbound key replications that failed during a rebalancer cycle"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, err
	}

	rebalancerDuration, err := m.Int64Histogram("mycorrhizal.rebalancer.duration",
		metric.WithDescription("Wall-clock time for a full rebalancer cycle"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	// ── Cluster / topology ────────────────────────────────────────────────────

	topologyChanges, err := m.Int64Counter("mycorrhizal.cluster.topology.changes",
		metric.WithDescription("Number of membership change events (node join or leave)"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}

	// ── Geo cache ─────────────────────────────────────────────────────────────

	geoSets, err := m.Int64Counter("mycorrhizal.geo.sets",
		metric.WithDescription("SetLocation calls"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	geoDeletes, err := m.Int64Counter("mycorrhizal.geo.deletes",
		metric.WithDescription("DeleteLocation calls"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	geoQueries, err := m.Int64Counter("mycorrhizal.geo.queries",
		metric.WithDescription("Geo spatial query calls by query type (bounding_box, radius)"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, err
	}

	geoQueryResults, err := m.Int64Histogram("mycorrhizal.geo.query.results",
		metric.WithDescription("Number of entries returned per geo query"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		return nil, err
	}

	geoQueryDuration, err := m.Int64Histogram("mycorrhizal.geo.query.duration",
		metric.WithDescription("Latency of geo spatial queries"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	geoReplicationSent, err := m.Int64Counter("mycorrhizal.geo.replication.sent",
		metric.WithDescription("Geo SET or DELETE frames broadcast to peers"),
		metric.WithUnit("{frame}"),
	)
	if err != nil {
		return nil, err
	}

	geoReplicationReceived, err := m.Int64Counter("mycorrhizal.geo.replication.received",
		metric.WithDescription("Geo replication frames received from peers"),
		metric.WithUnit("{frame}"),
	)
	if err != nil {
		return nil, err
	}

	geoReplicationDropped, err := m.Int64Counter("mycorrhizal.geo.replication.dropped",
		metric.WithDescription("Geo SET frames dropped because they arrived after the entry had already expired"),
		metric.WithUnit("{frame}"),
	)
	if err != nil {
		return nil, err
	}

	geoBucketSize, err := m.Int64UpDownCounter("mycorrhizal.geo.bucket.size",
		metric.WithDescription("Current number of location entries per geo bucket"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		return nil, err
	}

	return &metrics{
		gets:                          gets,
		sets:                          sets,
		deletes:                       deletes,
		ttlUpdates:                    ttlUpdates,
		hits:                          hits,
		misses:                        misses,
		lruEvictions:                  lruEvictions,
		ttlEvictions:                  ttlEvictions,
		bucketSize:                    bucketSize,
		duration:                      duration,
		errors:                        errors,
		replicaWrites:                 replicaWrites,
		quorumFailures:                quorumFailures,
		rebalancerCycles:              rebalancerCycles,
		rebalancerKeysProcessed:       rebalancerKeysProcessed,
		rebalancerKeysEvicted:         rebalancerKeysEvicted,
		rebalancerReplicationAttempts: rebalancerReplicationAttempts,
		rebalancerReplicationErrors:   rebalancerReplicationErrors,
		rebalancerDuration:            rebalancerDuration,
		topologyChanges:               topologyChanges,
		geoSets:                       geoSets,
		geoDeletes:                    geoDeletes,
		geoQueries:                    geoQueries,
		geoQueryResults:               geoQueryResults,
		geoQueryDuration:              geoQueryDuration,
		geoReplicationSent:            geoReplicationSent,
		geoReplicationReceived:        geoReplicationReceived,
		geoReplicationDropped:         geoReplicationDropped,
		geoBucketSize:                 geoBucketSize,
	}, nil
}

// ── Cache operation helpers ───────────────────────────────────────────────────

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

// ── Distributed write path helpers ───────────────────────────────────────────

// recordReplicaWrite records the outcome of a single replica write attempt.
// result should be "success" or "failure".
func (c *cache) recordReplicaWrite(bucket, result string) {
	c.metrics.replicaWrites.Add(c.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("result", result),
	))
}

// recordQuorumFailure records a quorum failure for a replicated operation.
func (c *cache) recordQuorumFailure(bucket string) {
	c.metrics.quorumFailures.Add(c.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}

// ── Rebalancer helpers ────────────────────────────────────────────────────────

// recordRebalancerCycle records the completion of a rebalancer cycle with its stats.
func (c *cache) recordRebalancerCycle(durationMs, keysProcessed, keysEvicted, replAttempts, replErrors int64) {
	c.metrics.rebalancerCycles.Add(c.ctx, 1)
	c.metrics.rebalancerDuration.Record(c.ctx, durationMs)
	c.metrics.rebalancerKeysProcessed.Add(c.ctx, keysProcessed)
	c.metrics.rebalancerKeysEvicted.Add(c.ctx, keysEvicted)
	c.metrics.rebalancerReplicationAttempts.Add(c.ctx, replAttempts)
	c.metrics.rebalancerReplicationErrors.Add(c.ctx, replErrors)
}

// ── Cluster / topology helpers ────────────────────────────────────────────────

// recordTopologyChange records a node join or leave event.
func (c *cache) recordTopologyChange(joined bool) {
	event := "leave"
	if joined {
		event = "join"
	}
	c.metrics.topologyChanges.Add(c.ctx, 1, metric.WithAttributes(
		attribute.String("event", event),
	))
}

// ── Geo cache helpers ─────────────────────────────────────────────────────────

func (g *geoCache) recordGeoSet(bucket string) {
	g.cache.metrics.geoSets.Add(g.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}

func (g *geoCache) recordGeoDelete(bucket string) {
	g.cache.metrics.geoDeletes.Add(g.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}

// trackGeoQuery records a spatial query and returns a done func that records
// result count and duration.
func (g *geoCache) trackGeoQuery(bucket, queryType string) func(resultCount int) {
	start := time.Now()
	g.cache.metrics.geoQueries.Add(g.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("query.type", queryType),
	))
	return func(resultCount int) {
		attrs := metric.WithAttributes(
			attribute.String("cache.bucket", bucket),
			attribute.String("query.type", queryType),
		)
		g.cache.metrics.geoQueryDuration.Record(g.ctx, time.Since(start).Milliseconds(), attrs)
		g.cache.metrics.geoQueryResults.Record(g.ctx, int64(resultCount), attrs)
	}
}

func (g *geoCache) recordGeoReplicationSent(bucket, operation string) {
	g.cache.metrics.geoReplicationSent.Add(g.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("operation", operation),
	))
}

func (g *geoCache) recordGeoReplicationReceived(bucket, operation string) {
	g.cache.metrics.geoReplicationReceived.Add(g.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
		attribute.String("operation", operation),
	))
}

func (g *geoCache) recordGeoReplicationDropped(bucket string) {
	g.cache.metrics.geoReplicationDropped.Add(g.ctx, 1, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}

func (g *geoCache) recordGeoBucketSize(bucket string, delta int64) {
	g.cache.metrics.geoBucketSize.Add(g.ctx, delta, metric.WithAttributes(
		attribute.String("cache.bucket", bucket),
	))
}
