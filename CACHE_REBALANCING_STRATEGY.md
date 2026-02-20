# Cache Rebalancing Strategy for Mycorrizal

## Executive Summary

This document analyzes cache rebalancing strategies for Mycorrizal's distributed cache (Mycel) when cluster topology changes due to nodes joining or leaving. With the planned implementation of 3-way replication, rebalancing becomes primarily a matter of **maintaining replica count** rather than preventing data loss, significantly simplifying the design.

**Recommended Approach**: Replica-aware rebalancing with background convergence + optional S3 snapshots for disaster recovery.

---

## Current State Analysis

### What Exists Today

**Cache Distribution** (`internal/mycel/ds.go:96-133`):
- **Rendezvous hashing** (highest random weight) determines primary node for each key
- Hash function: `SHA256(key || nodeId)` → uint64 score
- Nodes sorted by score (descending) → ordered list of potential replicas
- **Current limitation**: Only primary node (highest score) stores the key

**Cluster Membership Awareness** (`internal/nodosum/memberlist.go`):
- Memberlist provides delegate callbacks for topology changes:
  - `NotifyJoin(node)` - new node joins
  - `NotifyLeave(node)` - graceful departure
  - `NotifyUpdate(node)` - node metadata changes
  - **NotifyAlive(node)** - periodic health check
- QUIC connections established/closed automatically via delegates

**Critical Gap**:
- No replication implemented yet (planned for 3 replicas)
- No rebalancing logic when topology changes
- Cached primary node IDs in `nodeScoreHashMap` become stale on membership changes
- No invalidation mechanism for stale cache entries

### Key Files Involved

| Component | File | Purpose |
|-----------|------|---------|
| Cache layer | `internal/mycel/cache.go` | Get/Set/Delete operations, `isLocal()` |
| Hashing | `internal/mycel/ds.go` | Rendezvous hash, `getReplicas()` |
| Remote ops | `internal/mycel/remote.go` | Network calls to other nodes |
| Membership | `internal/nodosum/memberlist.go` | Delegate callbacks for join/leave |
| Lifecycle | `internal/mycel/mycel.go` | Startup/shutdown coordination |

---

## Replication-First Design

### Why Replication Changes Everything

With **3-way replication**, the rebalancing problem transforms from:
- ❌ "How do we prevent data loss when a node leaves?"

To:
- ✅ "How do we maintain 3 replicas for resilience?"

**Key Benefits**:
1. **Data survives node loss**: 2 other replicas still exist
2. **Fast shutdown**: Leaving node doesn't wait for transfers
3. **Crash tolerance**: Unexpected failures don't cause data loss
4. **Gradual rebalancing**: Can converge to 3 replicas over time (eventual consistency)

### Rendezvous Hashing with Replicas

The existing `getReplicas()` function already calculates the full sorted node list:

```go
// From ds.go:114-133
func (c *cache) getReplicas(key string) []replicaNode {
    nodes := c.app.Nodes()  // Get all connected nodes
    nodes = append(nodes, c.nodeId)  // Include self

    var scoredNodes []replicaNode
    for _, nodeId := range nodes {
        score := score(key, nodeId)  // SHA256 hash
        scoredNodes = append(scoredNodes, replicaNode{id: nodeId, score: score})
    }

    // Sort descending by score
    sort.Slice(scoredNodes, func(i, j int) bool {
        return scoredNodes[i].score > scoredNodes[j].score
    })

    return scoredNodes  // [0] = primary, [1] = replica 1, [2] = replica 2
}
```

**Current usage**: Only `scoredNodes[0]` (primary) is used.

**With replication**: Use `scoredNodes[0:3]` as the 3 replica nodes.

### Deterministic Replica Selection

Rendezvous hashing provides a **critical property**: All nodes independently calculate the same replica set for a given key.

```
Node A calculates getReplicas("user:123") → [NodeX, NodeY, NodeZ]
Node B calculates getReplicas("user:123") → [NodeX, NodeY, NodeZ]
Node C calculates getReplicas("user:123") → [NodeX, NodeY, NodeZ]
```

**No coordination needed** - every node knows which 3 nodes should store each key.

---

## Rebalancing Strategies

### Strategy 1: Replica-Aware Rebalancing (RECOMMENDED)

**Core Mechanism**: When topology changes, maintain 3 replicas per key through background convergence.

#### How It Works

**On Node Leave** (via `NotifyLeave` delegate):

1. **Invalidate cached mappings**:
   ```go
   c.nodeScoreHashMap.Lock()
   for key := range c.nodeScoreHashMap.data {
       delete(c.nodeScoreHashMap.data, key)  // Force recalculation
   }
   c.nodeScoreHashMap.Unlock()
   ```

2. **Trigger background rebalancer**:
   - Iterate through all keys in local cache
   - For each key, recalculate `getReplicas(key)`
   - Determine new top-3 replica nodes
   - If current node is still in top-3: keep the key
   - If current node no longer in top-3: delete locally (another node now responsible)
   - If new node appears in top-3: replicate to that node

3. **Convergence**:
   - Nodes independently execute this logic
   - Eventually, all keys have exactly 3 replicas on correct nodes
   - No central coordinator needed

**On Node Join** (via `NotifyJoin` delegate):

1. **Invalidate cached mappings** (same as leave)
2. **Passive integration**: New node starts empty
3. **Background rebalancer** on existing nodes:
   - Some keys will now have new node in their top-3
   - Those keys get replicated to new node
   - Old nodes may evict keys if no longer in top-3

**On Node Crash** (detected by memberlist health checks):
- Memberlist automatically calls `NotifyLeave` after failure detection timeout
- Same rebalancing logic as graceful leave
- Remaining 2 replicas ensure no data loss

#### Implementation Points

**Rebalancer Worker** (new file: `internal/mycel/rebalancer.go`):

```go
type rebalancer struct {
    cache *cache
    ctx context.Context
    wg *sync.WaitGroup
    logger *slog.Logger
    interval time.Duration  // e.g., 30 seconds
}

func (r *rebalancer) Start() {
    // Background worker that periodically checks all keys
    worker := worker.NewWorker(
        r.ctx,
        "mycel-rebalancer",
        r.wg,
        r.rebalanceTaskFunc,
        r.logger,
        r.interval,
    )
    go worker.Start()
}

func (r *rebalancer) rebalanceTaskFunc() error {
    // Iterate all buckets and keys
    for bucketName, bucket := range r.cache.buckets {
        current := bucket.dll.head.next
        for current != bucket.dll.tail {
            key := current.key
            fullKey := bucketName + key

            // Get current top-3 replicas
            replicas := r.cache.getReplicas(fullKey)[:3]

            // Check if current node should have this key
            shouldHaveKey := false
            for _, replica := range replicas {
                if replica.id == r.cache.nodeId {
                    shouldHaveKey = true
                    break
                }
            }

            if !shouldHaveKey {
                // Delete locally - no longer a replica
                r.cache.deleteLocal(bucketName, key)
            } else {
                // Ensure all 3 replicas have the key
                r.ensureReplicas(bucketName, key, current.value, replicas)
            }

            current = current.next
        }
    }
    return nil
}

func (r *rebalancer) ensureReplicas(bucket, key string, value []byte, replicas []replicaNode) {
    // For each replica node, verify they have the key
    for _, replica := range replicas {
        if replica.id == r.cache.nodeId {
            continue  // Already have it locally
        }

        // Check if replica has it (or just send it - PUT is idempotent)
        r.cache.setRemote(bucket, key, value, replica.id, 0)
    }
}
```

**Trigger on Membership Change**:

Modify `internal/nodosum/memberlist.go` to notify Mycel:

```go
func (d Delegate) NotifyLeave(node *memberlist.Node) {
    // Existing QUIC cleanup...
    d.closeQuicConnection(nodeID)

    // NEW: Notify all applications of topology change
    d.Nodosum.notifyTopologyChange()
}
```

In `internal/mycel/mycel.go`, add handler:

```go
func (m *Mycel) OnTopologyChange() {
    // Invalidate cached primary node mappings
    m.cache.nodeScoreHashMap.Lock()
    m.cache.nodeScoreHashMap.data = make(map[string]string)
    m.cache.nodeScoreHashMap.Unlock()

    // Trigger immediate rebalance (or let background worker handle it)
    m.rebalancer.TriggerImmediate()
}
```

#### Advantages

✅ **Simple**: No complex transfer protocols
✅ **Resilient**: Survives crashes (2 replicas remain)
✅ **Fast shutdown**: Leaving node doesn't wait
✅ **Eventual consistency**: Converges over time
✅ **Idempotent**: Rebalancer can run multiple times safely
✅ **Distributed**: No central coordinator

#### Disadvantages

⚠️ **Temporary under-replication**: Keys may have <3 replicas during convergence
⚠️ **Network overhead**: Background workers generate traffic
⚠️ **Tuning required**: Rebalancer interval affects convergence time

---

### Strategy 2: Active Rebalancing via NotifyLeave

**Alternative approach**: Leaving node actively transfers keys before shutdown.

#### How It Works

1. **Pre-shutdown hook** in `Mycel.Shutdown()`:
   - Calculate keys that need new homes
   - Transfer each key to new replica nodes
   - Wait for transfers with timeout (e.g., 30 seconds)
   - Proceed with shutdown regardless

2. **Implementation**:
   ```go
   func (m *Mycel) Shutdown() error {
       // NEW: Transfer keys before leaving
       m.transferKeysBeforeLeave(30 * time.Second)

       // Existing shutdown logic...
       return m.Nodosum.Shutdown()
   }
   ```

#### When to Use

- When minimizing under-replication window is critical
- When graceful shutdowns are the common case (not crashes)
- When network is reliable and fast

#### Limitations

❌ **Doesn't handle crashes**: Node dies before transferring
❌ **Delays shutdown**: Must wait for transfers
❌ **Complex failure handling**: What if transfers fail?
❌ **Wasted work**: Background rebalancer still needed for crashes

**Verdict**: Not recommended as primary strategy with replication. Use background rebalancing instead.

---

### Strategy 3: S3 Snapshot for Disaster Recovery

**Purpose**: Not for routine rebalancing, but for **cluster-wide recovery** scenarios.

#### Use Cases

1. **Entire cluster restart**: All nodes down simultaneously
2. **Majority failure**: Lost quorum, <2 replicas for many keys
3. **Audit/backup**: Periodic snapshots for compliance
4. **Development**: Snapshot production cache, restore to staging

#### How It Works

**Periodic Snapshots** (e.g., every 15 minutes):

```go
type snapshotWorker struct {
    cache *cache
    s3Client *s3.Client
    bucketName string
    interval time.Duration
}

func (s *snapshotWorker) createSnapshot() error {
    snapshot := &CacheSnapshot{
        NodeId: s.cache.nodeId,
        Timestamp: time.Now(),
        Buckets: make(map[string][]KeyValue),
    }

    // Serialize all buckets
    for bucketName, bucket := range s.cache.buckets {
        var keyValues []KeyValue
        current := bucket.dll.head.next
        for current != bucket.dll.tail {
            keyValues = append(keyValues, KeyValue{
                Key: current.key,
                Value: current.value,
                TTL: current.ttl,
            })
            current = current.next
        }
        snapshot.Buckets[bucketName] = keyValues
    }

    // Upload to S3
    data, _ := json.Marshal(snapshot)
    compressed := gzip.Compress(data)

    key := fmt.Sprintf("snapshots/%s/%d.json.gz", s.cache.nodeId, time.Now().Unix())
    return s.s3Client.PutObject(s.bucketName, key, compressed)
}
```

**Recovery Process**:

1. Detect under-replicated keys (have <3 replicas)
2. Query S3 for recent snapshots from all nodes
3. Download snapshots, extract missing keys
4. Replicate to appropriate nodes based on rendezvous hash

**On Graceful Shutdown** (optional):

```go
func (m *Mycel) Shutdown() error {
    // Create final snapshot before leaving
    m.snapshotWorker.createSnapshot()

    // Continue with normal shutdown...
    return m.Nodosum.Shutdown()
}
```

#### Advantages

✅ **Disaster recovery**: Handles total cluster loss
✅ **Decoupled**: Doesn't block normal operations
✅ **Audit trail**: Historical snapshots for compliance
✅ **Fast shutdown**: Snapshot async, don't wait

#### Disadvantages

⚠️ **External dependency**: Requires S3 or compatible storage
⚠️ **Staleness**: Snapshots are point-in-time, may miss recent writes
⚠️ **Cost**: S3 storage and transfer costs
⚠️ **Complexity**: Additional moving parts

**Recommendation**: Implement as **optional feature** for production deployments, not core rebalancing mechanism.

---

## Comparison Matrix

| Criteria | Background Rebalancer | Active Transfer on Leave | S3 Snapshot |
|----------|----------------------|--------------------------|-------------|
| **Handles crashes** | ✅ Yes | ❌ No | ✅ Yes |
| **Shutdown speed** | ✅ Fast | ❌ Slow (waits) | ✅ Fast |
| **Complexity** | 🟡 Medium | 🔴 High | 🔴 High |
| **External deps** | ✅ None | ✅ None | ❌ S3 required |
| **Consistency** | 🟡 Eventual | ✅ Strong (if succeeds) | 🔴 Eventual + stale |
| **Network overhead** | 🟡 Periodic | 🟡 On leave only | 🟢 Minimal |
| **Disaster recovery** | ❌ No | ❌ No | ✅ Yes |
| **Tuning required** | 🟡 Interval | 🟡 Timeout | 🟡 Frequency |

**Legend**: ✅ Good | 🟡 Acceptable | ❌ Poor | 🟢 Excellent | 🔴 Requires attention

---

## Recommended Architecture

### Primary Mechanism: Background Rebalancer

**Core implementation**:
1. Background worker runs every 30-60 seconds
2. Iterates all local keys, recalculates top-3 replicas
3. Ensures keys are on correct nodes
4. Deletes keys no longer owned
5. Replicates to new nodes when needed

**Triggers**:
- Periodic interval (30-60s)
- Membership change events (join/leave) → immediate execution
- Manual trigger via CLI (for testing/debugging)

### Optional Enhancement: S3 Snapshots

**When to enable**:
- Production deployments
- Regulatory/compliance requirements
- Multi-region setups
- Disaster recovery planning

**Implementation**:
- Periodic snapshots (15-30 minutes)
- Final snapshot on graceful shutdown
- Recovery CLI command to restore from S3
- Automatic under-replication detection

---

## Implementation Plan

### Phase 1: Replication Foundation (REQUIRED FIRST)

**Goal**: Modify cache operations to write to 3 nodes instead of 1.

**Files to modify**:
- `internal/mycel/cache.go`: Update `Put()` to replicate
- `internal/mycel/remote.go`: Batch replication calls
- `internal/mycel/ds.go`: Add `getReplicaNodes(key, count int)` helper

**Changes**:
```go
// In cache.go
func (c *cache) Put(bucket, key string, value []byte, ttl time.Duration) error {
    fullKey := bucket + key
    replicas := c.getReplicas(fullKey)[:3]  // Top 3 nodes

    var wg sync.WaitGroup
    errChan := make(chan error, 3)

    for _, replica := range replicas {
        wg.Add(1)
        go func(nodeId string) {
            defer wg.Done()
            if nodeId == c.nodeId {
                errChan <- c.setLocal(bucket, key, value, ttl)
            } else {
                errChan <- c.setRemote(bucket, key, value, nodeId, ttl)
            }
        }(replica.id)
    }

    wg.Wait()
    close(errChan)

    // Return first error (if any)
    for err := range errChan {
        if err != nil {
            return err
        }
    }
    return nil
}
```

**Read strategy** (choose one):
- **Option A**: Read from local replica if available, else random remote
- **Option B**: Read from primary (highest score) only
- **Option C**: Read from fastest responding replica (race)

Recommended: **Option A** (locality preference, lower latency)

### Phase 2: Background Rebalancer (CORE FEATURE)

**Goal**: Maintain 3 replicas as topology changes.

**New files**:
- `internal/mycel/rebalancer.go`: Rebalancer worker implementation
- `internal/mycel/rebalancer_test.go`: Test scenarios

**Integration points**:
- `internal/mycel/mycel.go`: Start/stop rebalancer
- `internal/nodosum/memberlist.go`: Hook into `NotifyLeave`/`NotifyJoin`

**Configuration** (add to `config.go`):
```go
type Config struct {
    // Existing fields...

    // Rebalancer settings
    RebalancerEnabled bool          // Default: true
    RebalancerInterval time.Duration // Default: 30s
    RebalancerBatchSize int         // Keys to process per cycle, default: 100
}
```

**Testing**:
- Unit tests: Verify replica calculation correctness
- Integration tests: Simulate node leave, verify convergence
- Chaos tests: Random joins/leaves, verify eventual 3 replicas

### Phase 3: Membership Event Propagation

**Goal**: Notify Mycel when cluster topology changes.

**Changes to Nodosum**:
- Add `TopologyChangeHandler` interface
- Applications can register handlers
- `NotifyJoin`/`NotifyLeave` triggers handlers

**In `internal/nodosum/nodosum.go`**:
```go
type TopologyChangeHandler interface {
    OnNodeJoin(nodeId string)
    OnNodeLeave(nodeId string)
}

func (n *Nodosum) RegisterTopologyHandler(handler TopologyChangeHandler) {
    n.topologyHandlers = append(n.topologyHandlers, handler)
}

func (n *Nodosum) notifyTopologyChange(event string, nodeId string) {
    for _, handler := range n.topologyHandlers {
        switch event {
        case "join":
            handler.OnNodeJoin(nodeId)
        case "leave":
            handler.OnNodeLeave(nodeId)
        }
    }
}
```

**In `internal/nodosum/memberlist.go`**:
```go
func (d Delegate) NotifyLeave(node *memberlist.Node) {
    nodeID := node.Name
    d.closeQuicConnection(nodeID)

    // Trigger topology change handlers
    d.Nodosum.notifyTopologyChange("leave", nodeID)
}
```

**Mycel implements handler**:
```go
func (m *Mycel) OnNodeJoin(nodeId string) {
    m.cache.invalidateCachedMappings()
    m.rebalancer.TriggerImmediate()
}

func (m *Mycel) OnNodeLeave(nodeId string) {
    m.cache.invalidateCachedMappings()
    m.rebalancer.TriggerImmediate()
}
```

### Phase 4: S3 Snapshot (OPTIONAL)

**Goal**: Disaster recovery and compliance.

**New files**:
- `internal/mycel/snapshot.go`: Snapshot creation/restoration
- `internal/mycel/snapshot_s3.go`: S3 integration
- `cmd/pulse/snapshot.go`: CLI commands

**Configuration**:
```go
type Config struct {
    // Existing fields...

    // Snapshot settings
    SnapshotEnabled bool           // Default: false
    SnapshotInterval time.Duration // Default: 15min
    SnapshotS3Bucket string        // S3 bucket name
    SnapshotS3Prefix string        // Key prefix, default: "mycorrizal-snapshots"
    SnapshotOnShutdown bool        // Default: true
}
```

**CLI commands**:
```bash
pulse snapshot create                    # Manual snapshot
pulse snapshot list                      # List available snapshots
pulse snapshot restore --from <timestamp> # Restore from snapshot
pulse snapshot verify                    # Check replica count
```

---

## Edge Cases & Considerations

### 1. Split Brain Scenarios

**Problem**: Network partition causes cluster fragmentation.

**Memberlist behavior**: Automatically handles via anti-entropy and merge protocols.

**Cache implication**:
- Two fragments may have different views of key ownership
- When partition heals, `NotifyMerge` is called
- Rebalancer converges both fragments to consistent state

**Conflict resolution**: Last-write-wins (TTL timestamps can help)

### 2. Rapid Topology Changes

**Problem**: Nodes joining/leaving faster than rebalancer interval.

**Solution**:
- Invalidate cache mappings immediately on each event
- Queue rebalance triggers (debounce rapid events)
- Increase rebalancer frequency during instability

**Adaptive interval**:
```go
if topologyChangesInLastMinute > 10 {
    rebalancerInterval = 10 * time.Second  // More frequent
} else {
    rebalancerInterval = 60 * time.Second  // Less frequent
}
```

### 3. Asymmetric Key Distribution

**Problem**: Rendezvous hashing doesn't guarantee uniform distribution.

**Impact**: Some nodes may store more keys than others.

**Mitigation**:
- Monitor key count per node (add metrics)
- Consider virtual nodes if severe imbalance occurs
- LRU eviction naturally balances cache size

**Not a critical issue**: Rendezvous hashing is reasonably uniform for large key sets.

### 4. TTL Synchronization

**Problem**: Each node independently manages TTLs for its replicas.

**Current state**: TTL eviction is node-local (`internal/mycel/task.go`)

**Concern**: One node might evict while others keep the key.

**Solution**:
- **Option A**: TTL embedded in key value, replicated with data
- **Option B**: Delete broadcasts - when one node evicts, notify replicas
- **Option C**: Accept divergence (eventual consistency via TTL)

**Recommendation**: Option C initially, Option B if strict consistency needed.

### 5. Rebalancer Thundering Herd

**Problem**: All nodes trigger rebalancing simultaneously on topology change.

**Impact**: Network spike, duplicate replication requests.

**Mitigation**:
- Stagger rebalancer execution with jitter
  ```go
  jitter := time.Duration(rand.Int63n(int64(5 * time.Second)))
  time.Sleep(jitter)
  ```
- Idempotent operations (SET is idempotent, safe to duplicate)
- Rate limiting on replication calls

### 6. Large Value Replication

**Problem**: Replicating large values (MB+) is expensive.

**Current**: No size limits in cache implementation.

**Solutions**:
- Add max value size to bucket config
- Stream large values in chunks
- Use S3 for large blobs, cache only references
- Compress values before replication

---

## Monitoring & Observability

### Metrics to Add

**Replication Health**:
- `mycel_replica_count{bucket}` - Histogram of replica counts per key (target: 3)
- `mycel_under_replicated_keys` - Count of keys with <3 replicas
- `mycel_over_replicated_keys` - Count of keys with >3 replicas

**Rebalancer Activity**:
- `mycel_rebalancer_runs_total` - Counter of rebalancer executions
- `mycel_rebalancer_keys_moved` - Keys transferred per run
- `mycel_rebalancer_duration_seconds` - Time per rebalancer cycle
- `mycel_rebalancer_errors_total` - Failed rebalancing attempts

**Topology**:
- `mycel_topology_changes_total{type}` - Join/leave events
- `mycel_cluster_size` - Current node count
- `mycel_node_key_count` - Keys stored per node

### Logging

**Key events to log**:
```go
logger.Info("rebalancer started", "interval", r.interval)
logger.Info("topology change detected", "event", "leave", "nodeId", nodeId)
logger.Info("key rebalanced", "bucket", bucket, "key", key, "from", oldNode, "to", newNode)
logger.Warn("replication failed", "bucket", bucket, "key", key, "target", nodeId, "error", err)
```

### Health Checks

Add endpoint to check replication health:
```go
GET /health/replication
{
  "status": "healthy",  // healthy | degraded | critical
  "total_keys": 10000,
  "under_replicated": 15,  // <3 replicas
  "over_replicated": 0,     // >3 replicas
  "target_replicas": 3,
  "cluster_size": 5
}
```

---

## Performance Considerations

### Network Bandwidth

**Worst case**: Node joins empty cluster of N nodes with K keys each.
- Total keys in cluster: N × K
- New node receives: K/N keys (rendezvous redistribution)
- Replication traffic: K/N × value_size

**Example**: 5 nodes, 100k keys/node, 1KB avg value
- New node receives: ~20k keys = 20MB
- Over 30s rebalancer interval = ~670KB/s

**Mitigation**: Batch replication, rate limiting

### CPU Impact

**Hashing cost**: SHA256 is fast (~300 MB/s on modern CPU)
- 1000 keys × 5 nodes = 5000 hash operations
- ~1ms total for scoring

**Sorting cost**: O(N log N) where N = node count
- Negligible for small clusters (<100 nodes)

### Memory Overhead

**Cached mappings**: `nodeScoreHashMap` stores primary node ID per key
- Memory: ~100 bytes per key (key string + node ID)
- 1M keys = ~100MB

**Snapshot memory**: Temporary during creation
- Full cache serialized in memory
- Recommend streaming to S3 for large caches

---

## Testing Strategy

### Unit Tests

1. **Rendezvous hash determinism**:
   - Same key + nodes → same ordering
   - Different nodes → different ordering

2. **Replica calculation**:
   - 5 nodes → top 3 correct
   - Node leaves → recalculation correct

3. **Rebalancer logic**:
   - Key moves to correct nodes
   - Deletes from wrong nodes
   - Handles empty cache gracefully

### Integration Tests

1. **3-node cluster**:
   - Start 3 nodes
   - Insert 100 keys
   - Verify each key on exactly 3 nodes

2. **Node leave**:
   - 4-node cluster with 100 keys
   - Stop 1 node gracefully
   - Verify remaining 3 nodes converge to 3 replicas each

3. **Node crash**:
   - 4-node cluster
   - Kill -9 one node
   - Verify memberlist detects failure
   - Verify rebalancer restores 3 replicas

### Chaos Tests

1. **Rapid churn**:
   - Random joins/leaves every 5 seconds
   - Verify eventual convergence to 3 replicas
   - Verify no data loss

2. **Network partition**:
   - Split cluster into two fragments
   - Insert keys in both fragments
   - Heal partition
   - Verify merge and rebalancing

---

## Migration Path

### Existing Deployments (if any)

If Mycorrizal is already deployed without replication:

**Phase 1**: Deploy replication code (backward compatible)
- Write to 3 nodes, read from local or any replica
- Existing single-copy keys still work
- New keys get replicated

**Phase 2**: Background migration
- Rebalancer detects under-replicated keys
- Gradually replicates to reach 3 copies
- Monitor `mycel_under_replicated_keys` until zero

**Phase 3**: Enable replication enforcement
- Reject writes if can't reach 3 replicas
- Strict consistency mode

---

## Conclusion

### Recommended Immediate Actions

1. ✅ **Implement 3-way replication** (Phase 1)
   - Modify Put/Get to use 3 nodes
   - Test thoroughly

2. ✅ **Build background rebalancer** (Phase 2)
   - Worker that maintains 3 replicas
   - Triggered by topology changes

3. ✅ **Add topology event propagation** (Phase 3)
   - Hook Mycel into memberlist delegates
   - Invalidate caches on join/leave

4. 🔲 **S3 snapshots** (Phase 4 - optional)
   - Implement for production deployments
   - Not critical for core functionality

### Why This Approach Wins

With **3-way replication + background rebalancing**:

✅ **Simple**: Fits existing architecture
✅ **Resilient**: Survives crashes (2 replicas remain)
✅ **Fast**: No shutdown delays
✅ **Consistent**: Eventual consistency acceptable
✅ **Scalable**: Distributed, no coordinator
✅ **Observable**: Clear metrics for health

The S3 snapshot approach is valuable as a **disaster recovery tool**, but should not be the primary rebalancing mechanism. Use it for:
- Total cluster failure recovery
- Compliance/audit requirements
- Development environment seeding

### Next Steps

1. Review and approve this plan
2. Implement Phase 1 (replication) first - critical foundation
3. Test thoroughly with integration tests
4. Implement Phase 2 (rebalancer) once replication proven
5. Consider Phase 4 (S3) for production hardening

---

## Appendix: Code Examples

### A. Complete Rebalancer Worker

```go
// internal/mycel/rebalancer.go
package mycel

import (
    "context"
    "sync"
    "time"
    "log/slog"
    "github.com/conamu/go-worker"
)

type Rebalancer struct {
    cache    *cache
    ctx      context.Context
    wg       *sync.WaitGroup
    logger   *slog.Logger
    interval time.Duration
    worker   *worker.Worker
    triggerChan chan struct{}
}

func NewRebalancer(cache *cache, ctx context.Context, wg *sync.WaitGroup, logger *slog.Logger, interval time.Duration) *Rebalancer {
    return &Rebalancer{
        cache:       cache,
        ctx:         ctx,
        wg:          wg,
        logger:      logger,
        interval:    interval,
        triggerChan: make(chan struct{}, 1),
    }
}

func (r *Rebalancer) Start() {
    r.worker = worker.NewWorker(
        r.ctx,
        "mycel-rebalancer",
        r.wg,
        r.rebalanceTask,
        r.logger,
        r.interval,
    )

    go r.worker.Start()

    // Listener for immediate triggers
    go r.listenForTriggers()
}

func (r *Rebalancer) listenForTriggers() {
    for {
        select {
        case <-r.ctx.Done():
            return
        case <-r.triggerChan:
            r.rebalanceTask()
        }
    }
}

func (r *Rebalancer) TriggerImmediate() {
    select {
    case r.triggerChan <- struct{}{}:
    default:
        // Already triggered, skip
    }
}

func (r *Rebalancer) rebalanceTask() error {
    r.logger.Info("rebalancer cycle started")
    startTime := time.Now()

    keysProcessed := 0
    keysMoved := 0
    keysDeleted := 0

    // Iterate all buckets
    r.cache.buckets.RLock()
    bucketsList := make([]*bucket, 0, len(r.cache.buckets.data))
    for _, b := range r.cache.buckets.data {
        bucketsList = append(bucketsList, b)
    }
    r.cache.buckets.RUnlock()

    for _, bucket := range bucketsList {
        bucket.dll.lock.RLock()
        current := bucket.dll.head.next

        for current != bucket.dll.tail {
            key := current.key
            value := current.value
            ttl := current.ttl

            fullKey := bucket.name + key

            // Calculate current replica nodes
            replicas := r.cache.getReplicas(fullKey)
            if len(replicas) < 3 {
                r.logger.Warn("insufficient nodes for replication", "key", key, "nodes", len(replicas))
                current = current.next
                continue
            }

            topThree := replicas[:3]

            // Check if current node should have this key
            shouldHaveKey := false
            for _, replica := range topThree {
                if replica.id == r.cache.nodeId {
                    shouldHaveKey = true
                    break
                }
            }

            if !shouldHaveKey {
                // Delete locally - no longer a replica
                r.logger.Info("deleting key no longer owned", "bucket", bucket.name, "key", key)
                bucket.dll.lock.RUnlock()
                r.cache.deleteLocal(bucket.name, key)
                bucket.dll.lock.RLock()
                keysDeleted++
            } else {
                // Ensure all replicas have the key
                for _, replica := range topThree {
                    if replica.id == r.cache.nodeId {
                        continue
                    }

                    // Replicate to remote node (idempotent)
                    err := r.cache.setRemote(bucket.name, key, value, replica.id, ttl)
                    if err != nil {
                        r.logger.Warn("replication failed",
                            "bucket", bucket.name,
                            "key", key,
                            "target", replica.id,
                            "error", err)
                    } else {
                        keysMoved++
                    }
                }
            }

            keysProcessed++
            current = current.next
        }

        bucket.dll.lock.RUnlock()
    }

    duration := time.Since(startTime)
    r.logger.Info("rebalancer cycle completed",
        "duration", duration,
        "processed", keysProcessed,
        "moved", keysMoved,
        "deleted", keysDeleted)

    return nil
}
```

### B. Modified Put with Replication

```go
// internal/mycel/cache.go
func (c *cache) Put(bucket, key string, value []byte, ttl time.Duration) error {
    fullKey := bucket + key

    // Get top 3 replica nodes
    replicas := c.getReplicas(fullKey)
    if len(replicas) < 3 {
        return fmt.Errorf("insufficient nodes for replication: need 3, have %d", len(replicas))
    }

    topThree := replicas[:3]

    // Write to all 3 replicas in parallel
    var wg sync.WaitGroup
    errChan := make(chan error, 3)

    for _, replica := range topThree {
        wg.Add(1)
        go func(nodeId string) {
            defer wg.Done()

            var err error
            if nodeId == c.nodeId {
                // Local write
                err = c.setLocal(bucket, key, value, ttl)
            } else {
                // Remote write
                err = c.setRemote(bucket, key, value, nodeId, ttl)
            }

            if err != nil {
                errChan <- fmt.Errorf("replication to %s failed: %w", nodeId, err)
            }
        }(replica.id)
    }

    wg.Wait()
    close(errChan)

    // Collect errors
    var errors []error
    for err := range errChan {
        errors = append(errors, err)
    }

    // Success if at least 2/3 replicas succeeded (quorum)
    if len(errors) > 1 {
        return fmt.Errorf("replication failed on multiple nodes: %v", errors)
    }

    return nil
}
```

### C. Topology Change Handler Interface

```go
// internal/nodosum/topology.go
package nodosum

type TopologyChangeHandler interface {
    OnNodeJoin(nodeId string)
    OnNodeLeave(nodeId string)
}

type topologyHandlers struct {
    sync.RWMutex
    handlers []TopologyChangeHandler
}

func (n *Nodosum) RegisterTopologyHandler(handler TopologyChangeHandler) {
    n.topologyHandlers.Lock()
    defer n.topologyHandlers.Unlock()
    n.topologyHandlers.handlers = append(n.topologyHandlers.handlers, handler)
}

func (n *Nodosum) notifyTopologyChange(event, nodeId string) {
    n.topologyHandlers.RLock()
    defer n.topologyHandlers.RUnlock()

    for _, handler := range n.topologyHandlers.handlers {
        switch event {
        case "join":
            go handler.OnNodeJoin(nodeId)
        case "leave":
            go handler.OnNodeLeave(nodeId)
        }
    }
}
```

---

**Document Version**: 1.0
**Author**: Claude Code
**Date**: 2025-12-29
**Status**: Proposed
