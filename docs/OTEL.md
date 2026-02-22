# OpenTelemetry Instrumentation

## Cache Layer (`mycorrizal.cache.*`)

| Instrument Name | Type | Unit | Attributes | Description |
|---|---|---|---|---|
| `mycorrizal.cache.gets` | Counter | `{request}` | `cache.bucket`, `cache.locality` | Total get operations |
| `mycorrizal.cache.sets` | Counter | `{request}` | `cache.bucket`, `cache.locality` | Total set operations |
| `mycorrizal.cache.deletes` | Counter | `{request}` | `cache.bucket`, `cache.locality` | Total delete operations |
| `mycorrizal.cache.ttl_updates` | Counter | `{request}` | `cache.bucket`, `cache.locality` | Total SetTtl operations |
| `mycorrizal.cache.hits` | Counter | `{request}` | `cache.bucket`, `cache.locality` | Successful get lookups |
| `mycorrizal.cache.misses` | Counter | `{request}` | `cache.bucket`, `cache.locality` | Failed get lookups |
| `mycorrizal.cache.lru_evictions` | Counter | `{item}` | `cache.bucket` | Items dropped due to maxLen |
| `mycorrizal.cache.ttl_evictions` | Counter | `{item}` | `cache.bucket` | Items expired by TTL worker |
| `mycorrizal.cache.bucket.size` | UpDownCounter | `{item}` | `cache.bucket` | Current item count per bucket |
| `mycorrizal.cache.bucket.capacity` | Gauge | `{item}` | `cache.bucket` | Configured maxLen per bucket |
| `mycorrizal.cache.duration` | Histogram | `ms` | `cache.bucket`, `cache.locality` | Operation latency split by locality |
| `mycorrizal.cache.errors` | Counter | `{error}` | `cache.bucket`, `cache.locality` | Failed operations split by locality |

## Geospatial Layer (`mycorrizal.geo.*`)

| Instrument Name | Type | Unit | Attributes | Description |
|---|---|---|---|---|
| `mycorrizal.geo.query.duration` | Histogram | `ms` | `geo.query_type` | Query latency by type |
| `mycorrizal.geo.query.results` | Histogram | `{item}` | `geo.query_type` | Result set size per query |
| `mycorrizal.geo.query.nodes_consulted` | Histogram | `{node}` | `geo.query_type` | Nodes hit per query |
| `mycorrizal.geo.index.size` | UpDownCounter | `{item}` | `geo.shard` | Total entries in index |
| `mycorrizal.geo.index.shard.size` | UpDownCounter | `{item}` | `geo.shard` | Entries per shard |
| `mycorrizal.geo.index.updates` | Counter | `{request}` | `geo.operation` | Inserts/deletes/moves |
| `mycorrizal.geo.cache.locality` | Counter | `{request}` | `geo.query_type`, `cache.locality` | Local vs remote geo cache hits |
| `mycorrizal.geo.index.reindex.duration` | Histogram | `ms` | `geo.shard` | Time to rebuild spatial structures |

## Attribute Value Conventions

| Attribute | Values |
|---|---|
| `cache.locality` | `"local"`, `"remote"` |
| `cache.operation` | `"get"`, `"set"`, `"delete"`, `"setttl"` | _(used for logging only, not an OTEL attribute)_ |
| `geo.query_type` | `"radius"`, `"bbox"`, `"nearest"` |
| `geo.operation` | `"insert"`, `"delete"`, `"move"` |

## Useful Prometheus Queries

### Gets per second
```promql
rate(mycorrizal_cache_gets_total[1m])
```

### Gets per second by bucket
```promql
rate(mycorrizal_cache_gets_total[1m]) by (cache_bucket)
```

### Hit rate per bucket
```promql
rate(mycorrizal_cache_hits_total[1m])
/
rate(mycorrizal_cache_gets_total[1m])
```

### Bucket fill ratio
```promql
mycorrizal_cache_bucket_size / mycorrizal_cache_bucket_capacity
```

### Eviction pressure (evictions vs sets)
```promql
rate(mycorrizal_cache_lru_evictions_total[1m])
/
rate(mycorrizal_cache_sets_total[1m])
```

### Op latency p99 by locality
```promql
histogram_quantile(0.99, rate(mycorrizal_cache_duration_bucket[1m])) by (cache_bucket, cache_locality)
```

### Error rate by locality
```promql
rate(mycorrizal_cache_errors_total[1m]) by (cache_bucket, cache_locality)
```

## Implementation Notes

- Instruments are created once at initialization in `newCacheMetrics()` and stored on the `cache` struct — never created per-operation
- Recording is wrapped in `trackOp()` helpers to keep cache logic readable
- Key-level metrics are intentionally omitted to avoid cardinality explosion — bucket is the finest granularity
