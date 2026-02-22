To finish the cache:
- ~~implement the otel metrics~~ — done: all cache instruments initialized once in `newMetrics()`, track helpers in `otel.go`, remaining wiring: call `recordLruEviction` in `local.go`, `recordTtlEviction` in `task.go`, `recordBucketSize` on set/delete in `local.go`
- need to implement the use of remote operations to achieve the cache distribution
- implement the cache rebalancer
- implement geospatial index fully replicated on all nodes
- change to a better and faster hashing algorithm instead of sha256 for more efficient operation
- implement methods for getting locations, point queries and box lookup queries. Also write, and delete locations.