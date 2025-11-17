# Hybrid Ring-Star Network Topology Architecture

**Date:** 2025-11-16
**Status:** Design Proposal
**Author:** Architecture Discussion

## Table of Contents

1. [Problem Statement](#problem-statement)
2. [Design Goals](#design-goals)
3. [Proposed Solution](#proposed-solution)
4. [Architecture Deep Dive](#architecture-deep-dive)
5. [Implementation Plan](#implementation-plan)
6. [Technical Specifications](#technical-specifications)
7. [Migration Path](#migration-path)
8. [Open Questions](#open-questions)

---

## Problem Statement

### Current Architecture Limitations

Mycorrizal currently implements a **star topology** where all nodes connect to all other nodes (full mesh). This is defined in `internal/nodosum/nodosum.go:18-28`:

```go
/*
SCOPE

- Discover Instances via Consul API/DNS-SD
- Establish Connections in Star Network Topology (all nodes have a connection to all nodes)
- Manage connections and keep them up X
- Provide communication interface to abstract away the cluster
  (this should feel like one big App, even though it could be spread on 10 nodes/instances)
*/
```

**Scaling Problem:**
- 10 nodes = 90 total connections (each node maintains 9 connections)
- 100 nodes = 9,900 total connections (each node maintains 99 connections)
- 1000 nodes = 999,000 total connections (each node maintains 999 connections)

This doesn't scale beyond small clusters due to:
- Connection overhead (TCP sockets, goroutines for read/write loops)
- Memory consumption (buffers per connection)
- Discovery broadcast storms (UDP HELLO packets to all nodes)
- Multiplexer bottlenecks (routing to hundreds of connections)

### Requirements for Scale

To support **hundreds to thousands of nodes**, we need:
1. **Bounded connections per node** (e.g., 5-7 maximum regardless of cluster size)
2. **Maintained connectivity** (every node can reach every other node)
3. **Fault tolerance** (redundant paths, no single point of failure)
4. **Deterministic topology** (no central coordinator required)
5. **Cache efficiency** (existing rendezvous hashing still works)

---

## Design Goals

### Primary Objectives

1. **Limit connections to ~5-7 per node** regardless of cluster size
2. **Enable multi-hop routing** for nodes not directly connected
3. **Form logical clusters (cohorts)** of 3-6 nodes in full mesh
4. **Connect cohorts via bridge nodes** forming a ring backbone
5. **Self-organizing topology** without central coordination
6. **Maintain existing API compatibility** for applications and cache

### Non-Goals (Out of Scope)

- Changing the cache eviction algorithm (LRU/TTL stays the same)
- Modifying application messaging API (existing `Send()` interface preserved)
- Implementing the Cytoplasm event bus layer (separate future work)
- External load balancing or service mesh integration

---

## Proposed Solution

### Hybrid Ring-Star Topology

The architecture combines two network patterns:

1. **Star topology within cohorts**: Small groups (3-6 nodes) maintain full mesh connectivity
2. **Ring topology between cohorts**: Cohorts connect via designated bridge nodes

```
Visual Representation:

Cohort 0          Cohort 1          Cohort 2
┌─────────┐      ┌─────────┐      ┌─────────┐
│  A   B  │      │  D   E  │      │  G   H  │
│   \ /   │◄────►│   \ /   │◄────►│   \ /   │
│    C    │      │    F    │      │    I    │
└─────────┘      └─────────┘      └─────────┘
     ▲                                   │
     └───────────────────────────────────┘
              (ring wraps around)

Legend:
- Lines within boxes: Full mesh (star topology)
- Arrows between boxes: Bridge connections
- Each node: 2-4 cohort peers + 1-2 bridge connections = 5-6 total
```

### Key Innovation

**Reuse existing rendezvous hashing** from `internal/mycel/ds.go:97-126` for:
- Deterministic cohort assignment (consistent hash ring)
- Bridge node selection
- Cache key placement (already working)

This ensures all nodes independently calculate the same topology without coordination.

---

## Architecture Deep Dive

### Component 1: Consistent Hash Ring

**Purpose:** Deterministically assign nodes to positions on a virtual ring.

**Implementation Location:** New file `internal/nodosum/topology.go`

```go
type TopologyManager struct {
    sync.RWMutex
    nodeId       string
    ring         *HashRing
    cohortSize   int        // e.g., 4 nodes per cohort
    bridgeCount  int        // e.g., 2 bridge nodes per cohort
    routingTable *RoutingTable
}

type HashRing struct {
    nodes      []string          // All known nodes sorted by hash
    positions  map[string]uint64 // nodeId -> hash position on ring
}

// Algorithm:
// 1. Hash each node ID (SHA256, reuse from mycel.score())
// 2. Sort nodes by hash value
// 3. Assign to cohorts based on position
//
// Example with 9 nodes, cohort size 3:
// Ring: [A, B, C, D, E, F, G, H, I] (sorted by hash)
// Cohort 0: [A, B, C]
// Cohort 1: [D, E, F]
// Cohort 2: [G, H, I]
```

**Critical Design Decision:**
Use the **same hash function** as Mycel's rendezvous hashing (`mycel/ds.go:97`):

```go
func (m *mycel) score(key, nodeId string) uint64 {
    h := sha256.New()
    h.Write([]byte(key))
    h.Write([]byte(nodeId))
    sum := h.Sum(nil)
    return binary.LittleEndian.Uint64(sum[:8])
}
```

For topology, we hash just the nodeId without a key:

```go
func hashNode(nodeId string) uint64 {
    h := sha256.New()
    h.Write([]byte(nodeId))
    sum := h.Sum(nil)
    return binary.LittleEndian.Uint64(sum[:8])
}
```

### Component 2: Cohort Formation

**Purpose:** Group adjacent nodes on the ring into small clusters.

**Cohort membership rules:**
1. Nodes at positions `i`, `i+1`, `i+2`, ... `i+cohortSize-1` form a cohort
2. Each node connects to all other nodes in its cohort (full mesh)
3. Cohorts wrap around the ring (last cohort includes first nodes)

**Example:**
```go
// 10 nodes, cohort size = 3
Nodes by hash: [A, B, C, D, E, F, G, H, I, J]

Cohort 0: [A, B, C]    // positions 0, 1, 2
Cohort 1: [D, E, F]    // positions 3, 4, 5
Cohort 2: [G, H, I]    // positions 6, 7, 8
Cohort 3: [J, A, B]    // positions 9, 0, 1 (wraps around)
```

**Connection calculation for node C:**
- Same cohort: A, B (2 connections)
- Bridge role: If C is designated bridge, connect to cohort 1 (1-2 connections)
- **Total: 3-4 connections**

### Component 3: Bridge Node Selection

**Purpose:** Each cohort designates nodes to connect to adjacent cohorts.

**Selection algorithm:**
```go
// Deterministic bridge selection (all nodes calculate same result)
func (tm *TopologyManager) getBridgeNodes(cohort []string, count int) []string {
    // Option 1: Lowest node IDs (lexicographic sort)
    sort.Strings(cohort)
    return cohort[:count]

    // Option 2: Hash-based selection for better distribution
    // Score each node, take top N
}
```

**Bridge connection pattern:**
```
Cohort 0 bridges: [A, B]
Cohort 1 bridges: [D, E]
Cohort 2 bridges: [G, H]

Connections:
- A connects to D (forward bridge)
- D connects to G (forward bridge)
- G connects to A (forward bridge, wraps ring)

- B connects to E (redundant forward bridge)
- E connects to H (redundant forward bridge)
- H connects to B (redundant forward bridge, wraps)

Result: 2 redundant ring paths for fault tolerance
```

**Maximum connections per node:**
```
Regular cohort member:
  - Cohort peers: cohortSize - 1 (e.g., 3 nodes = 2 peers)

Bridge node:
  - Cohort peers: cohortSize - 1
  - Bridge connections: 2 (forward + backward, or 2 forward for redundancy)
  - Total: cohortSize + 1 (e.g., 3 + 1 = 4 connections)
```

### Component 4: Routing Protocol

**Purpose:** Enable messages to traverse multiple hops to reach destination.

**New frame types** in `internal/nodosum/protocol.go`:

```go
const (
    // Existing frame types
    SYSTEM      uint8 = 1
    DATA        uint8 = 2
    HELLO       uint8 = 3
    HELLO_ACK   uint8 = 4

    // New routing frame types
    ROUTE_DATA   uint8 = 10  // Multi-hop routed message
    ROUTE_QUERY  uint8 = 11  // Ask neighbor for route to destination
    ROUTE_UPDATE uint8 = 12  // Gossip routing table changes
)

type RoutedFrame struct {
    Version       uint8
    Type          uint8      // ROUTE_DATA
    SourceNodeId  string     // Original sender
    DestNodeId    string     // Final destination
    TTL           uint8      // Hop limit (prevent infinite loops)
    HopCount      uint8      // Number of hops taken
    Hops          []string   // Path trace for debugging
    ApplicationID string     // Which app is sending this
    Payload       []byte     // Actual message
}
```

**Routing algorithm:**

```go
// In multiplexer (internal/nodosum/mux.go)
func (n *Nodosum) routeFrame(frame *RoutedFrame) error {
    // 1. Check if we are the destination
    if frame.DestNodeId == n.nodeId {
        // Deliver to local application
        return n.deliverLocal(frame)
    }

    // 2. Check if we have direct connection
    if conn, ok := n.connections.Load(frame.DestNodeId); ok {
        // Send directly
        return n.sendDirect(conn, frame)
    }

    // 3. Check TTL (prevent routing loops)
    if frame.TTL == 0 {
        return errors.New("TTL exceeded")
    }
    frame.TTL--
    frame.HopCount++
    frame.Hops = append(frame.Hops, n.nodeId)

    // 4. Look up next hop in routing table
    nextHop := n.topology.routingTable.GetNextHop(frame.DestNodeId)
    if nextHop == "" {
        return errors.New("no route to destination")
    }

    // 5. Forward to next hop
    conn, ok := n.connections.Load(nextHop)
    if !ok {
        return errors.New("next hop connection not found")
    }
    return n.sendDirect(conn, frame)
}
```

**Routing table structure:**

```go
type RoutingTable struct {
    sync.RWMutex

    // nextHop maps destination nodeId -> next hop nodeId
    // Example for node C:
    //   C -> A: direct (same cohort)
    //   C -> B: direct (same cohort)
    //   C -> D: via B (B is bridge to cohort 1)
    //   C -> E: via B
    //   C -> F: via B
    nextHop map[string]string

    // cohortMap tracks which nodes are in which cohorts
    // Used to rebuild routes when topology changes
    cohortMap map[int][]string

    // reachable tracks which nodes are known to be alive
    reachable map[string]time.Time  // nodeId -> last seen
}
```

**Route calculation example:**

```
Topology:
  Cohort 0: [A, B, C] (B is bridge)
  Cohort 1: [D, E, F] (E is bridge)
  Cohort 2: [G, H, I] (H is bridge)

Node C routing table:
  Destination | Next Hop | Reason
  ------------|----------|---------------------------
  A           | direct   | Same cohort
  B           | direct   | Same cohort
  D           | B        | B bridges to cohort 1
  E           | B        | B bridges to cohort 1
  F           | B        | B bridges to cohort 1
  G           | B        | B -> E -> H (2 hops away)
  H           | B        | B -> E -> H
  I           | B        | B -> E -> H

Maximum hops in this topology:
  C -> I requires 3 hops: C -> B -> E -> H -> I (4 hops)

With ring wrap-around optimization:
  Can be reduced by going backward on ring
```

### Component 5: Gossip Protocol

**Purpose:** Synchronize topology state across the cluster without central coordination.

**Gossip message types:**

```go
type GossipMessage struct {
    Type      GossipMsgType
    Sender    string
    Timestamp time.Time
    Data      []byte
}

type GossipMsgType uint8
const (
    GOSSIP_HEARTBEAT   GossipMsgType = 1  // I'm alive
    GOSSIP_JOIN        GossipMsgType = 2  // New node joining
    GOSSIP_LEAVE       GossipMsgType = 3  // Node leaving gracefully
    GOSSIP_TOPOLOGY    GossipMsgType = 4  // Cohort membership changed
    GOSSIP_ROUTE       GossipMsgType = 5  // Routing table update
)

type HeartbeatData struct {
    NodeId      string
    Cohort      int
    IsBridge    bool
    Connections []string  // Current connection list
}
```

**Gossip propagation:**

```go
// New file: internal/nodosum/gossip.go

type GossipManager struct {
    sync.RWMutex
    nodeId          string
    topology        *TopologyManager
    app             *Application
    heartbeatTicker *time.Ticker
    nodeStates      map[string]*NodeState
}

type NodeState struct {
    LastSeen    time.Time
    Cohort      int
    IsBridge    bool
    Connections []string
}

// Gossip worker runs every 3-5 seconds
func (gm *GossipManager) gossipWorker(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Send heartbeat to all direct neighbors
            msg := gm.createHeartbeat()
            gm.app.Send(msg, []string{})  // Broadcast to connected nodes

            // Check for dead nodes (no heartbeat in 15s)
            gm.detectFailures()
        }
    }
}

func (gm *GossipManager) detectFailures() {
    gm.Lock()
    defer gm.Unlock()

    now := time.Now()
    for nodeId, state := range gm.nodeStates {
        if now.Sub(state.LastSeen) > 15*time.Second {
            // Node appears dead, remove from topology
            gm.topology.RemoveNode(nodeId)
            delete(gm.nodeStates, nodeId)
        }
    }
}
```

**Failure detection and recovery:**

```
Timeline:
T=0s:   Node E crashes
T=5s:   Nodes D and F detect missing heartbeat
T=10s:  D and F gossip E's failure to their neighbors
T=15s:  All nodes mark E as unreachable
T=20s:  Routing tables updated, E removed from cohort 1
T=25s:  If E was bridge, cohort elects new bridge (F becomes bridge)
T=30s:  New topology propagated via gossip
```

### Component 6: Integration with Existing Mycel Cache

**Current cache architecture** (`internal/mycel/`):

The cache already uses rendezvous hashing to determine which node stores a key:

```go
// mycel/ds.go:106-126
func (m *mycel) getReplicas(key string, replicas int) []replicaNode {
    nodes := m.app.Nodes()  // Gets all connected nodes
    var scoredNodes []replicaNode

    for i, nodeId := range nodes {
        if i >= replicas {
            break
        }
        nodeScore := m.score(key, nodeId)
        scoredNodes = append(scoredNodes, replicaNode{
            id:    nodeId,
            score: nodeScore,
        })
    }

    sort.Slice(scoredNodes, func(i, j int) bool {
        return scoredNodes[i].score > scoredNodes[j].score
    })

    return scoredNodes
}
```

**Problem with routing:**
Currently, cache operations expect direct connections to all nodes. With hybrid topology, we need routing.

**Solution:**

Modify cache operations to use routing:

```go
// mycel/cache.go (modified)

func (m *mycel) Get(bucketName, key string) (any, error) {
    // 1. Calculate primary replica using rendezvous hashing (unchanged)
    replicas := m.getReplicas(bucketName+":"+key, m.replicationFactor)
    primaryNode := replicas[0].id

    // 2. If primary is local, return from local cache
    if primaryNode == m.nodeId {
        return m.getLocal(bucketName, key)
    }

    // 3. If primary is direct neighbor, send request directly
    if m.app.IsDirectConnection(primaryNode) {
        return m.getRemote(primaryNode, bucketName, key)
    }

    // 4. NEW: Use routing to reach remote node
    return m.getRemoteRouted(primaryNode, bucketName, key)
}

func (m *mycel) getRemoteRouted(targetNode, bucketName, key string) (any, error) {
    // Create routed request frame
    req := &CacheRequest{
        Operation: CACHE_GET,
        Bucket:    bucketName,
        Key:       key,
    }

    // Send via routing protocol (ROUTE_DATA frame)
    resp, err := m.app.SendRouted(targetNode, req, 30*time.Second)
    if err != nil {
        // Try replica if primary unreachable
        return m.tryReplica(bucketName, key, 1)
    }

    return resp, nil
}
```

**Cache replication with routing:**

```
Scenario: Node A wants to write to cache
1. Calculate replicas: [E, H, C] (via rendezvous hash)
2. Node A's topology:
   - E: Not direct connection, route via bridge
   - H: Not direct connection, route via bridge
   - C: Direct connection (same cohort)
3. Send writes:
   - A -> C: Direct send
   - A -> (route) -> E: Via routing (A -> B -> E)
   - A -> (route) -> H: Via routing (A -> B -> E -> H)
```

---

## Implementation Plan

### Phase 1: Core Topology (Week 1)

**Goal:** Build deterministic cohort assignment and connection limiting.

**Tasks:**

1. **Create `internal/nodosum/topology.go`**
   - Implement `TopologyManager` struct
   - Implement `HashRing` with consistent hashing
   - Implement `calculateCohort(nodeId)` function
   - Implement `getCohortMembers(nodeId)` function
   - Implement `getBridgeNodes(cohort)` function

2. **Modify `internal/nodosum/config.go`**
   - Add `TopologyMode` config option
   - Add `CohortSize` (default: 4)
   - Add `BridgeCount` (default: 2)
   - Add `MaxConnections` (calculated from cohort + bridge)

3. **Modify `internal/nodosum/nodosum.go`**
   - Add `topology *TopologyManager` field
   - Initialize topology manager in `New()`
   - Modify `Start()` to filter discovered nodes
   - Only connect to cohort peers + bridges (not all nodes)

4. **Test with 9 nodes locally**
   - Verify cohort formation
   - Verify connection counts (should be ~4-6 per node)
   - Verify deterministic topology (all nodes agree)

**Acceptance criteria:**
- [ ] 9 nodes form 3 cohorts correctly
- [ ] Each node has ≤ 6 connections
- [ ] Topology is identical across all nodes (deterministic)
- [ ] Existing star topology still works (fallback mode)

### Phase 2: Routing Protocol (Week 2)

**Goal:** Enable multi-hop message delivery.

**Tasks:**

5. **Create `internal/nodosum/routing.go`**
   - Implement `RoutingTable` struct
   - Implement `calculateRoutes()` based on topology
   - Implement `GetNextHop(destId)` lookup
   - Implement route caching and updates

6. **Modify `internal/nodosum/protocol.go`**
   - Add `ROUTE_DATA`, `ROUTE_QUERY`, `ROUTE_UPDATE` frame types
   - Implement `RoutedFrame` encoding/decoding
   - Add TTL field to prevent infinite loops

7. **Modify `internal/nodosum/mux.go`**
   - Add routing logic to multiplexer
   - Intercept outgoing frames, check if routing needed
   - Intercept incoming frames, check if we're destination or relay
   - Implement `routeFrame()` function
   - Add hop count tracking

8. **Modify `internal/nodosum/application.go`**
   - Add `SendRouted(destNodeId, payload)` method
   - Add `IsDirectConnection(nodeId)` helper
   - Maintain backward compatibility with existing `Send()`

9. **Test routing with 9 nodes**
   - Send message from node A (cohort 0) to node I (cohort 2)
   - Verify message traverses: A -> B (bridge) -> E (bridge) -> H -> I
   - Verify hop count and path tracking
   - Test TTL exhaustion (prevent infinite loops)

**Acceptance criteria:**
- [ ] Messages route across cohorts successfully
- [ ] Routing table builds correctly from topology
- [ ] TTL prevents infinite loops
- [ ] Existing applications work without code changes

### Phase 3: Gossip and Failure Recovery (Week 3)

**Goal:** Handle dynamic topology changes and node failures.

**Tasks:**

10. **Create `internal/nodosum/gossip.go`**
    - Implement `GossipManager` struct
    - Implement heartbeat broadcasts (every 5s)
    - Implement failure detection (15s timeout)
    - Implement topology update propagation

11. **Integrate gossip with topology**
    - Register `SYSTEM-TOPOLOGY` application
    - Connect gossip manager to topology manager
    - Trigger route recalculation on topology changes
    - Implement node join/leave handling

12. **Implement bridge failover**
    - Detect when bridge node fails
    - Elect new bridge from cohort
    - Update routing tables cluster-wide
    - Test redundancy (multiple bridge nodes)

13. **Testing failure scenarios**
    - Kill bridge node, verify traffic reroutes
    - Kill non-bridge node, verify minimal impact
    - Network partition: split cohort, verify detection
    - Rejoin scenario: bring node back, verify reintegration

**Acceptance criteria:**
- [ ] Heartbeats propagate every 5s
- [ ] Dead nodes detected within 15s
- [ ] Routing tables update automatically
- [ ] Bridge failover happens automatically
- [ ] No central coordinator needed

### Phase 4: Cache Integration (Week 4)

**Goal:** Make Mycel cache work transparently with routing.

**Tasks:**

14. **Modify `internal/mycel/cache.go`**
    - Add `getRemoteRouted()` method
    - Add `putRemoteRouted()` method
    - Fall back to replicas if primary unreachable
    - Add timeout handling for routed requests

15. **Add cache request/response protocol**
    - Define `CACHE_GET`, `CACHE_PUT`, `CACHE_DELETE` message types
    - Implement request/response correlation (request IDs)
    - Handle timeouts and retries

16. **Test distributed cache operations**
    - Write to node A, read from node I (cross-cohort)
    - Verify rendezvous hashing still works
    - Test replica failover
    - Test TTL eviction across cohorts

**Acceptance criteria:**
- [ ] Cache operations work across cohorts
- [ ] Rendezvous hashing unchanged
- [ ] Replica failover works
- [ ] Performance acceptable (< 50ms for 2-hop)

---

## Technical Specifications

### Configuration Options

```go
// config.go

type TopologyMode int
const (
    TOPOLOGY_STAR          TopologyMode = 0  // Current: all-to-all
    TOPOLOGY_HYBRID_RING   TopologyMode = 1  // New: cohorts + ring
)

type Config struct {
    // ... existing fields ...

    // Topology configuration
    TopologyMode      TopologyMode
    CohortSize        int           // Default: 4 nodes per cohort
    BridgeCount       int           // Default: 2 bridge nodes per cohort
    MaxConnections    int           // Calculated: cohortSize + bridgeCount

    // Routing configuration
    DefaultTTL        uint8         // Default: 10 hops
    RoutingTableTTL   time.Duration // How long to cache routes

    // Gossip configuration
    HeartbeatInterval time.Duration // Default: 5s
    FailureTimeout    time.Duration // Default: 15s
}
```

### File Structure

```
internal/nodosum/
├── nodosum.go           # Modified: Add topology manager
├── conn.go              # Unchanged
├── mux.go               # Modified: Add routing logic
├── protocol.go          # Modified: Add routing frame types
├── application.go       # Modified: Add SendRouted()
├── acl.go               # Unchanged
├── discoverDns.go       # Unchanged
├── discoverConsul.go    # Unchanged
├── topology.go          # NEW: Hash ring and cohort logic
├── routing.go           # NEW: Routing table and forwarding
└── gossip.go            # NEW: Failure detection and sync

internal/mycel/
├── mycel.go             # Unchanged
├── cache.go             # Modified: Add routed cache operations
├── ds.go                # Unchanged (rendezvous hash reused)
└── task.go              # Unchanged
```

### API Compatibility

**Existing Application API (preserved):**

```go
// Applications continue to work unchanged
app := mycorrizal.RegisterApplication("my-service")
app.SetReceiveFunc(func(payload []byte) error {
    // Handle messages
})

// Direct send (if nodes are connected)
app.Send(data, []string{"node-id-1"})

// Broadcast (to all reachable nodes, may use routing)
app.Send(data, []string{})
```

**New Routing API (optional):**

```go
// Explicitly use routing
resp, err := app.SendRouted("remote-node-id", request, 30*time.Second)

// Check if direct connection exists
if app.IsDirectConnection("node-id") {
    // Fast path
} else {
    // Use routing
}
```

**Cache API (unchanged externally):**

```go
cache := mycorrizal.Cache()
cache.CreateBucket("sessions", 15*time.Minute, 10000)
cache.Put("sessions", "key", value, 0)  // Transparently routes if needed
cache.Get("sessions", "key")            // Transparently routes if needed
```

---

## Migration Path

### Backward Compatibility

**Option 1: Configuration flag (recommended)**

```go
// Old clusters continue with star topology
config := mycorrizal.GetDefaultConfig()
config.TopologyMode = mycorrizal.TOPOLOGY_STAR  // Explicit opt-in

// New clusters use hybrid topology
config.TopologyMode = mycorrizal.TOPOLOGY_HYBRID_RING
config.CohortSize = 4
config.BridgeCount = 2
```

**Option 2: Automatic mode selection**

```go
// Automatically choose topology based on cluster size
func (cfg *Config) autoSelectTopology(discoveredNodes int) {
    if discoveredNodes <= 10 {
        cfg.TopologyMode = TOPOLOGY_STAR  // Small cluster, use full mesh
    } else {
        cfg.TopologyMode = TOPOLOGY_HYBRID_RING  // Large cluster, use hybrid
    }
}
```

### Upgrade Procedure

**Rolling upgrade strategy:**

1. **Deploy new version with star topology** (no behavior change)
2. **Monitor cluster health** (ensure no regressions)
3. **Enable hybrid topology on subset of nodes** (e.g., 25%)
4. **Gradually increase percentage** until all nodes upgraded
5. **Mixed-mode support:** Nodes with hybrid topology can still accept connections from star-topology nodes

**Mixed-mode compatibility:**

```go
// During transition, support both topologies
func (n *Nodosum) handleConnection(nodeId string) {
    // Check peer's topology mode via handshake
    peerMode := n.getPeerTopologyMode(nodeId)

    if peerMode == TOPOLOGY_STAR {
        // Accept connection even if we're using hybrid
        // This node will act as bridge to star-topology nodes
    }
}
```

---

## Open Questions

### 1. Cohort Size Tuning

**Question:** What is the optimal cohort size?

**Trade-offs:**
- **Smaller cohorts (2-3 nodes):** Fewer connections per node, but more hops between nodes
- **Larger cohorts (5-7 nodes):** More direct connections, but higher connection count

**Recommendation:** Start with `cohortSize = 4`, make configurable.

**Testing needed:**
- Benchmark message latency vs cohort size
- Measure connection overhead (memory, CPU)
- Test failure scenarios (what if cohort size < 3?)

### 2. Bridge Count

**Question:** How many bridge nodes per cohort?

**Trade-offs:**
- **1 bridge:** Minimal connections, single point of failure
- **2 bridges:** Good redundancy, reasonable overhead
- **3+ bridges:** High redundancy, diminishing returns

**Recommendation:** Start with `bridgeCount = 2`, make configurable.

### 3. Routing Algorithm Optimization

**Question:** Should we use shortest path routing or load balancing?

**Options:**

**A) Shortest path (simple)**
```go
// Always route via closest bridge
nextHop = routing.GetShortestPath(dest)
```

**B) Load balancing (complex)**
```go
// Distribute traffic across multiple bridges
nextHop = routing.GetLeastLoadedPath(dest)
```

**Recommendation:** Start with shortest path, add load balancing in future optimization phase.

### 4. Cache Consistency with Routing

**Question:** How to handle cache writes when primary node is unreachable?

**Scenarios:**

**A) Write fails if primary unreachable**
```go
if !canReachPrimary {
    return ErrPrimaryUnreachable
}
```

**B) Write to replica, mark as stale**
```go
if !canReachPrimary {
    writeToReplica()
    markAsStale()  // Sync later when primary returns
}
```

**Recommendation:** Start with option A (fail fast), implement option B (eventual consistency) in Phase 4.

### 5. Cluster Size Limits

**Question:** What's the maximum practical cluster size?

**Analysis:**

With `cohortSize = 4`, `bridgeCount = 2`:
- Each node: 3 cohort peers + 2 bridge connections = 5 total connections
- 100 nodes: 25 cohorts, max 3 hops between any two nodes
- 1000 nodes: 250 cohorts, max 4-5 hops between any two nodes
- 10,000 nodes: 2,500 cohorts, max 5-6 hops between any two nodes

**Latency impact:**
- Assume 1ms per hop
- 100 nodes: < 3ms routing overhead
- 1000 nodes: < 5ms routing overhead
- 10,000 nodes: < 6ms routing overhead

**Recommendation:** Test up to 1,000 nodes in Phase 3, plan for 10,000+ in future.

### 6. Gossip Protocol Efficiency

**Question:** Will gossip protocol scale to 1000+ nodes?

**Concerns:**
- Heartbeat storms (1000 nodes * 5s interval = 200 heartbeats/sec)
- Gossip propagation delay (how long to detect failure?)
- Network bandwidth consumption

**Optimizations to consider:**
- **Gossip fanout:** Only gossip to subset of neighbors
- **Epidemic broadcasting:** Exponential propagation (fast but redundant)
- **Stable leader:** One node per cohort coordinates gossip (centralizes)

**Recommendation:** Start with simple broadcast gossip, optimize in Phase 3 based on measurements.

### 7. Security Implications

**Question:** Does routing introduce new security risks?

**Concerns:**
- **Man-in-the-middle:** Bridge nodes can inspect routed traffic
- **Routing poisoning:** Malicious node advertises false routes
- **Amplification attacks:** Crafted routing loops cause CPU exhaustion

**Mitigations:**
- **End-to-end encryption:** Encrypt application payloads before routing
- **Signed routing updates:** Authenticate gossip messages with shared secret
- **TTL enforcement:** Strict hop limits (already planned)
- **Rate limiting:** Limit routed frames per second

**Recommendation:** Add to security review checklist in Phase 2.

### 8. Monitoring and Observability

**Question:** How to debug routing issues in production?

**Required metrics:**
- Current topology (cohort assignments)
- Routing table state (per node)
- Hop count histogram
- Routing failures (no route, TTL exceeded)
- Bridge node health

**Tooling needed:**
- CLI command to visualize topology: `mycorrizal topology show`
- Trace route command: `mycorrizal trace-route <source> <dest>`
- Routing table dump: `mycorrizal routes <node-id>`

**Recommendation:** Add observability in Phase 3, expose via Pulse CLI.

---

## Cohort Discovery Algorithms: Detailed Comparison

This section provides an in-depth analysis of two algorithmic approaches for deterministic cohort self-discovery. Both approaches allow nodes to independently calculate identical cohort assignments without coordination.

### Overview of Approaches

**Option 1: Simple Position-Based Hash Ring**
- Hash each node ID to get a position on the ring
- Sort nodes by hash value
- Assign cohorts sequentially based on position
- Simple modulo arithmetic: `cohort = position / cohortSize`

**Option 2: Rendezvous Hashing (Highest Random Weight)**
- For each cohort, score all nodes using cohort ID as key
- Nodes with highest scores for a cohort become members
- Reuses existing Mycel cache placement algorithm
- More even distribution across cohorts

---

### Algorithm 1: Position-Based Hash Ring

#### Conceptual Model

Think of nodes arranged on a circular ring where position is determined by hashing the node ID:

```
        Node A (hash: 0x1234)
              |
    Node I ---|--- Node B (hash: 0x2345)
  (hash: 0x9ABC) |
              |
         Node H --- Node C (hash: 0x3456)
       (hash: 0x89AB)
              |
         Node G --- Node D (hash: 0x4567)
       (hash: 0x789A)
              |
         Node F --- Node E
       (hash: 0x6789)  (hash: 0x5678)
```

After sorting by hash: `[A, B, C, D, E, F, G, H, I]`

With `cohortSize = 3`:
- Cohort 0: positions 0-2 → [A, B, C]
- Cohort 1: positions 3-5 → [D, E, F]
- Cohort 2: positions 6-8 → [G, H, I]

#### Implementation

```go
// In topology.go

type HashRing struct {
    sortedNodes []string          // Nodes sorted by hash
    positions   map[string]int    // nodeId -> position in sorted array
    hashes      map[string]uint64 // nodeId -> hash value
}

// hashNode calculates a node's position on the ring
func hashNode(nodeId string) uint64 {
    h := sha256.New()
    h.Write([]byte(nodeId))
    sum := h.Sum(nil)
    return binary.LittleEndian.Uint64(sum[:8])
}

// buildHashRing creates a sorted ring from all discovered nodes
func (tm *TopologyManager) buildHashRing(allNodes []string) *HashRing {
    ring := &HashRing{
        positions: make(map[string]int),
        hashes:    make(map[string]uint64),
    }

    // Hash all nodes
    type nodeHash struct {
        id   string
        hash uint64
    }
    nodeHashes := make([]nodeHash, len(allNodes))

    for i, nodeId := range allNodes {
        h := hashNode(nodeId)
        nodeHashes[i] = nodeHash{id: nodeId, hash: h}
        ring.hashes[nodeId] = h
    }

    // Sort by hash value
    sort.Slice(nodeHashes, func(i, j int) bool {
        return nodeHashes[i].hash < nodeHashes[j].hash
    })

    // Build sorted list and position map
    ring.sortedNodes = make([]string, len(nodeHashes))
    for i, nh := range nodeHashes {
        ring.sortedNodes[i] = nh.id
        ring.positions[nh.id] = i
    }

    return ring
}

// calculateMyCohort determines which cohort this node belongs to
func (tm *TopologyManager) calculateMyCohort(ring *HashRing) []string {
    myPos := ring.positions[tm.nodeId]
    cohortNum := myPos / tm.cohortSize

    // Calculate cohort boundaries
    start := cohortNum * tm.cohortSize
    end := start + tm.cohortSize
    if end > len(ring.sortedNodes) {
        end = len(ring.sortedNodes)
    }

    // Return cohort members (excluding myself)
    cohort := ring.sortedNodes[start:end]
    peers := make([]string, 0, len(cohort)-1)
    for _, nodeId := range cohort {
        if nodeId != tm.nodeId {
            peers = append(peers, nodeId)
        }
    }

    return peers
}

// getBridgeNodes determines which nodes in my cohort are bridges
func (tm *TopologyManager) getBridgeNodes(ring *HashRing) ([]string, bool) {
    myPos := ring.positions[tm.nodeId]
    cohortNum := myPos / tm.cohortSize

    start := cohortNum * tm.cohortSize
    end := start + tm.cohortSize
    if end > len(ring.sortedNodes) {
        end = len(ring.sortedNodes)
    }

    cohort := ring.sortedNodes[start:end]

    // Select first N nodes in cohort as bridges (deterministic)
    bridges := make([]string, 0, tm.bridgeCount)
    for i := 0; i < tm.bridgeCount && i < len(cohort); i++ {
        bridges = append(bridges, cohort[i])
    }

    // Check if I'm a bridge
    iAmBridge := false
    for _, b := range bridges {
        if b == tm.nodeId {
            iAmBridge = true
            break
        }
    }

    return bridges, iAmBridge
}

// getBridgeTargets calculates which nodes bridges should connect to
func (tm *TopologyManager) getBridgeTargets(ring *HashRing) []string {
    myPos := ring.positions[tm.nodeId]
    cohortNum := myPos / tm.cohortSize
    totalCohorts := (len(ring.sortedNodes) + tm.cohortSize - 1) / tm.cohortSize

    // Connect to next cohort (wraps around)
    nextCohort := (cohortNum + 1) % totalCohorts
    nextStart := nextCohort * tm.cohortSize
    nextEnd := nextStart + tm.cohortSize
    if nextEnd > len(ring.sortedNodes) {
        nextEnd = len(ring.sortedNodes)
    }

    // Get bridge nodes from next cohort
    nextCohortNodes := ring.sortedNodes[nextStart:nextEnd]
    targets := make([]string, 0, tm.bridgeCount)
    for i := 0; i < tm.bridgeCount && i < len(nextCohortNodes); i++ {
        targets = append(targets, nextCohortNodes[i])
    }

    return targets
}
```

#### Worked Example

**Scenario:** 10 nodes join cluster, `cohortSize = 3`, `bridgeCount = 2`

```
Step 1: Hash all node IDs
  node-alpha:   0x1A2B3C4D → position 0
  node-beta:    0x2B3C4D5E → position 1
  node-gamma:   0x3C4D5E6F → position 2
  node-delta:   0x4D5E6F70 → position 3
  node-epsilon: 0x5E6F7081 → position 4
  node-zeta:    0x6F708192 → position 5
  node-eta:     0x708192A3 → position 6
  node-theta:   0x8192A3B4 → position 7
  node-iota:    0x92A3B4C5 → position 8
  node-kappa:   0xA3B4C5D6 → position 9

Step 2: Sort by hash (already sorted above)

Step 3: Assign cohorts
  Cohort 0 (pos 0-2): [alpha, beta, gamma]
  Cohort 1 (pos 3-5): [delta, epsilon, zeta]
  Cohort 2 (pos 6-8): [eta, theta, iota]
  Cohort 3 (pos 9):   [kappa]  ← incomplete cohort!

Step 4: Select bridges (first 2 nodes per cohort)
  Cohort 0 bridges: [alpha, beta]
  Cohort 1 bridges: [delta, epsilon]
  Cohort 2 bridges: [eta, theta]
  Cohort 3 bridges: [kappa]  ← only 1 bridge

Step 5: Calculate connections for node "gamma"
  - My position: 2
  - My cohort: 0
  - Cohort peers: [alpha, beta] (2 connections)
  - Am I bridge? No (I'm position 2, bridges are 0 and 1)
  - Total connections: 2

Step 6: Calculate connections for node "alpha" (bridge)
  - My position: 0
  - My cohort: 0
  - Cohort peers: [beta, gamma] (2 connections)
  - Am I bridge? Yes
  - Bridge targets: cohort 1 bridges = [delta, epsilon] (2 connections)
  - Total connections: 4
```

#### Edge Cases

**Incomplete final cohort:**
```
11 nodes, cohortSize = 3:
  Cohort 0: [A, B, C] (3 nodes)
  Cohort 1: [D, E, F] (3 nodes)
  Cohort 2: [G, H, I] (3 nodes)
  Cohort 3: [J, K]    (2 nodes) ← incomplete

Solution: Treat as regular cohort, just fewer members
  - Cohort 3 members still connect to each other
  - Select min(2, len(cohort)) bridges
  - Works fine, just slightly less redundancy
```

**Very small clusters:**
```
4 nodes, cohortSize = 3:
  Cohort 0: [A, B, C] (3 nodes)
  Cohort 1: [D]       (1 node)

Node D is isolated! Solution:
  - Set minimum cohort size = 2
  - Or merge incomplete cohorts back to previous cohort
  - Or special case: clusters < cohortSize use star topology
```

---

### Algorithm 2: Rendezvous Hashing (HRW)

#### Conceptual Model

Instead of assigning nodes to positions and then grouping, we assign nodes to cohorts based on their affinity score for each cohort.

For each cohort, we ask: "Which nodes have the highest score for this cohort?"

```
Cohort 0 scores:
  score("cohort-0", node-A) = 0xF123... → rank 1 (highest)
  score("cohort-0", node-B) = 0xE234... → rank 2
  score("cohort-0", node-C) = 0xD345... → rank 3
  score("cohort-0", node-D) = 0x9456... → rank 4
  ...
  → Cohort 0 members: [A, B, C] (top 3)

Cohort 1 scores:
  score("cohort-1", node-D) = 0xF789... → rank 1 (highest)
  score("cohort-1", node-E) = 0xE89A... → rank 2
  score("cohort-1", node-F) = 0xD9AB... → rank 3
  score("cohort-1", node-A) = 0x6ABC... → rank 4
  ...
  → Cohort 1 members: [D, E, F] (top 3)
```

This naturally distributes nodes more evenly across cohorts.

#### Implementation

```go
// In topology.go

type RendezvousHashRing struct {
    allNodes    []string
    cohorts     map[int][]string          // cohortNum -> member nodes
    nodeCohort  map[string]int            // nodeId -> cohort number
    cohortCount int
}

// score calculates affinity between a cohort and a node
// Reuses the exact same function from mycel/ds.go
func (tm *TopologyManager) score(cohortKey, nodeId string) uint64 {
    h := sha256.New()
    h.Write([]byte(cohortKey))
    h.Write([]byte(nodeId))
    sum := h.Sum(nil)
    return binary.LittleEndian.Uint64(sum[:8])
}

type scoredNode struct {
    id    string
    score uint64
}

// buildRendezvousHashRing assigns nodes to cohorts using HRW
func (tm *TopologyManager) buildRendezvousHashRing(allNodes []string) *RendezvousHashRing {
    ring := &RendezvousHashRing{
        allNodes:   allNodes,
        cohorts:    make(map[int][]string),
        nodeCohort: make(map[string]int),
    }

    numCohorts := (len(allNodes) + tm.cohortSize - 1) / tm.cohortSize
    ring.cohortCount = numCohorts

    // Track which nodes are already assigned
    assigned := make(map[string]bool)

    // For each cohort, select nodes with highest scores
    for cohortNum := 0; cohortNum < numCohorts; cohortNum++ {
        cohortKey := fmt.Sprintf("cohort-%d", cohortNum)

        // Score all unassigned nodes for this cohort
        var scored []scoredNode
        for _, nodeId := range allNodes {
            if !assigned[nodeId] {
                s := tm.score(cohortKey, nodeId)
                scored = append(scored, scoredNode{id: nodeId, score: s})
            }
        }

        // Sort by score (highest first)
        sort.Slice(scored, func(i, j int) bool {
            return scored[i].score > scored[j].score
        })

        // Take top N nodes for this cohort
        memberCount := tm.cohortSize
        if len(scored) < memberCount {
            memberCount = len(scored)
        }

        cohortMembers := make([]string, memberCount)
        for i := 0; i < memberCount; i++ {
            nodeId := scored[i].id
            cohortMembers[i] = nodeId
            ring.nodeCohort[nodeId] = cohortNum
            assigned[nodeId] = true
        }

        ring.cohorts[cohortNum] = cohortMembers
    }

    return ring
}

// calculateMyCohort finds which cohort this node belongs to
func (tm *TopologyManager) calculateMyCohortHRW(ring *RendezvousHashRing) []string {
    myCohort, exists := ring.nodeCohort[tm.nodeId]
    if !exists {
        return []string{}
    }

    cohortMembers := ring.cohorts[myCohort]

    // Return peers (excluding myself)
    peers := make([]string, 0, len(cohortMembers)-1)
    for _, nodeId := range cohortMembers {
        if nodeId != tm.nodeId {
            peers = append(peers, nodeId)
        }
    }

    return peers
}

// getBridgeNodesHRW selects bridges using rendezvous hash
func (tm *TopologyManager) getBridgeNodesHRW(ring *RendezvousHashRing) ([]string, bool) {
    myCohort := ring.nodeCohort[tm.nodeId]
    cohortMembers := ring.cohorts[myCohort]

    // Score each member for "bridge" role
    bridgeKey := fmt.Sprintf("cohort-%d-bridge", myCohort)
    var scored []scoredNode
    for _, nodeId := range cohortMembers {
        s := tm.score(bridgeKey, nodeId)
        scored = append(scored, scoredNode{id: nodeId, score: s})
    }

    // Sort by score, take top N as bridges
    sort.Slice(scored, func(i, j int) bool {
        return scored[i].score > scored[j].score
    })

    bridgeCount := tm.bridgeCount
    if len(scored) < bridgeCount {
        bridgeCount = len(scored)
    }

    bridges := make([]string, bridgeCount)
    iAmBridge := false
    for i := 0; i < bridgeCount; i++ {
        bridges[i] = scored[i].id
        if scored[i].id == tm.nodeId {
            iAmBridge = true
        }
    }

    return bridges, iAmBridge
}

// getBridgeTargetsHRW calculates which nodes to connect to
func (tm *TopologyManager) getBridgeTargetsHRW(ring *RendezvousHashRing) []string {
    myCohort := ring.nodeCohort[tm.nodeId]
    nextCohort := (myCohort + 1) % ring.cohortCount

    nextCohortMembers := ring.cohorts[nextCohort]

    // Get bridges from next cohort
    bridgeKey := fmt.Sprintf("cohort-%d-bridge", nextCohort)
    var scored []scoredNode
    for _, nodeId := range nextCohortMembers {
        s := tm.score(bridgeKey, nodeId)
        scored = append(scored, scoredNode{id: nodeId, score: s})
    }

    sort.Slice(scored, func(i, j int) bool {
        return scored[i].score > scored[j].score
    })

    targetCount := tm.bridgeCount
    if len(scored) < targetCount {
        targetCount = len(scored)
    }

    targets := make([]string, targetCount)
    for i := 0; i < targetCount; i++ {
        targets[i] = scored[i].id
    }

    return targets
}
```

#### Worked Example

**Scenario:** Same 10 nodes, `cohortSize = 3`, `bridgeCount = 2`

```
Step 1: Calculate scores for Cohort 0
  score("cohort-0", node-alpha)   = 0xF1A2B3C4 → rank 1
  score("cohort-0", node-beta)    = 0xE2B3C4D5 → rank 2
  score("cohort-0", node-gamma)   = 0xD3C4D5E6 → rank 3
  score("cohort-0", node-delta)   = 0x94D5E6F7 → rank 7
  score("cohort-0", node-epsilon) = 0x75E6F708 → rank 9
  ...

  Cohort 0 members: [alpha, beta, gamma] (top 3)
  Mark as assigned: {alpha, beta, gamma}

Step 2: Calculate scores for Cohort 1 (excluding assigned)
  score("cohort-1", node-delta)   = 0xF4D5E6F7 → rank 1
  score("cohort-1", node-epsilon) = 0xE5E6F708 → rank 2
  score("cohort-1", node-zeta)    = 0xD6F70819 → rank 3
  score("cohort-1", node-eta)     = 0x8770819A → rank 5
  ...

  Cohort 1 members: [delta, epsilon, zeta] (top 3)
  Mark as assigned: {alpha, beta, gamma, delta, epsilon, zeta}

Step 3: Calculate scores for Cohort 2
  score("cohort-2", node-eta)   = 0xF770819A → rank 1
  score("cohort-2", node-theta) = 0xE8819AAB → rank 2
  score("cohort-2", node-iota)  = 0xD919AABC → rank 3
  ...

  Cohort 2 members: [eta, theta, iota] (top 3)
  Mark as assigned: {..., eta, theta, iota}

Step 4: Calculate scores for Cohort 3
  Only kappa remains unassigned

  Cohort 3 members: [kappa] (1 node)

Step 5: Select bridges for Cohort 0
  score("cohort-0-bridge", alpha) = 0xA1B2C3D4 → rank 2
  score("cohort-0-bridge", beta)  = 0xF2C3D4E5 → rank 1 ← highest
  score("cohort-0-bridge", gamma) = 0x73D4E5F6 → rank 3

  Cohort 0 bridges: [beta, alpha] (top 2)

Step 6: Connections for node "gamma"
  - My cohort: 0
  - Cohort peers: [alpha, beta] (2 connections)
  - Am I bridge? No
  - Total: 2 connections

Step 7: Connections for node "beta" (bridge)
  - My cohort: 0
  - Cohort peers: [alpha, gamma] (2 connections)
  - Am I bridge? Yes
  - Next cohort (1) bridges: score all cohort 1 members for "cohort-1-bridge"
    → [delta, epsilon] (2 connections)
  - Total: 4 connections
```

#### Advantages Over Position-Based

**Better distribution when nodes are removed:**
```
Position-based (10 → 9 nodes):
  Node F removed from middle
  Before: Cohort 1 = [D, E, F]
  After:  All cohorts shift! G moves from cohort 2 → cohort 1

  Result: Cascading reconnections across entire cluster

Rendezvous-based (10 → 9 nodes):
  Node F removed
  Before: Cohort 1 = [D, E, F]
  After:  Cohort 1 = [D, E, G] (G scores highest among remaining)

  Result: Only cohort 1 changes, others stable
```

**More uniform load distribution:**
- Position-based can have "hot spots" if hashes cluster
- Rendezvous naturally spreads nodes across cohorts

---

### Performance Comparison

| Metric | Position-Based | Rendezvous (HRW) |
|--------|----------------|------------------|
| **Time Complexity (initial)** | O(N log N) sort | O(N² log N) for all cohorts |
| **Time Complexity (lookup)** | O(1) hash + map | O(1) map lookup |
| **Space Complexity** | O(N) positions | O(N + C) nodes + cohorts |
| **Churn on node add** | Low (append to ring) | Medium (re-score 1 cohort) |
| **Churn on node remove** | High (positions shift) | Low (only 1 cohort changes) |
| **Load distribution** | Can cluster | Very even |
| **Implementation complexity** | Simple | Moderate |
| **Reuses Mycel code** | No | Yes (same `score()` function) |

**Recommendation:** Use **Rendezvous hashing** if cluster will have frequent membership changes. Use **Position-based** if simplicity and initialization speed matter more.

---

### Hybrid Approach: Best of Both Worlds

Combine both algorithms to get stability AND even distribution:

```go
type HybridHashRing struct {
    // Use position-based for initial assignment
    positionRing *HashRing

    // Use rendezvous for bridge selection only
    tm *TopologyManager
}

// buildHybridHashRing combines approaches
func (tm *TopologyManager) buildHybridHashRing(allNodes []string) *HybridHashRing {
    ring := &HybridHashRing{
        positionRing: tm.buildHashRing(allNodes),
        tm:           tm,
    }
    return ring
}

// Cohort assignment: use simple position-based (fast, simple)
func (hr *HybridHashRing) calculateMyCohort() []string {
    return hr.tm.calculateMyCohort(hr.positionRing)
}

// Bridge selection: use rendezvous (better distribution)
func (hr *HybridHashRing) getBridgeNodes() ([]string, bool) {
    myPos := hr.positionRing.positions[hr.tm.nodeId]
    cohortNum := myPos / hr.tm.cohortSize

    start := cohortNum * hr.tm.cohortSize
    end := start + hr.tm.cohortSize
    if end > len(hr.positionRing.sortedNodes) {
        end = len(hr.positionRing.sortedNodes)
    }

    cohort := hr.positionRing.sortedNodes[start:end]

    // Use rendezvous hashing to select bridges from cohort
    bridgeKey := fmt.Sprintf("cohort-%d-bridge", cohortNum)
    var scored []scoredNode
    for _, nodeId := range cohort {
        s := hr.tm.score(bridgeKey, nodeId)
        scored = append(scored, scoredNode{id: nodeId, score: s})
    }

    sort.Slice(scored, func(i, j int) bool {
        return scored[i].score > scored[j].score
    })

    bridgeCount := hr.tm.bridgeCount
    if len(scored) < bridgeCount {
        bridgeCount = len(scored)
    }

    bridges := make([]string, bridgeCount)
    iAmBridge := false
    for i := 0; i < bridgeCount; i++ {
        bridges[i] = scored[i].id
        if scored[i].id == hr.tm.nodeId {
            iAmBridge = true
        }
    }

    return bridges, iAmBridge
}
```

**Hybrid advantages:**
- ✅ Fast initialization (only sort once)
- ✅ Simple cohort assignment (position-based)
- ✅ Better bridge distribution (rendezvous)
- ✅ Reuses Mycel `score()` function
- ✅ Balance between simplicity and quality

**Recommendation:** Use the hybrid approach for production implementation.

---

### Summary and Recommendations

**For initial implementation (Phase 1):**
- Use **Position-Based Hash Ring** for simplicity
- Get the basic topology working and tested
- Focus on correctness over optimization

**For production deployment:**
- Use **Hybrid Approach** combining both algorithms
- Position-based for cohort assignment (simple, fast)
- Rendezvous for bridge selection (even load distribution)
- Leverages existing Mycel infrastructure

**For future optimization:**
- Monitor cohort size variance in production
- If uneven distribution becomes an issue, switch to pure Rendezvous
- Add metrics to track bridge load and adjust algorithm accordingly

**Code organization:**
```go
// topology.go structure

// Core interface (implementation-agnostic)
type TopologyRing interface {
    GetCohortPeers(nodeId string) []string
    GetBridgeNodes(nodeId string) (bridges []string, iAmBridge bool)
    GetBridgeTargets(nodeId string) []string
}

// Implementations
type PositionBasedRing struct { ... }
type RendezvousHashRing struct { ... }
type HybridHashRing struct { ... }

// Factory method allows switching algorithms via config
func NewTopologyRing(mode string, nodes []string, cfg *Config) TopologyRing {
    switch mode {
    case "position":
        return &PositionBasedRing{ ... }
    case "rendezvous":
        return &RendezvousHashRing{ ... }
    case "hybrid":
        return &HybridHashRing{ ... }
    default:
        return &HybridHashRing{ ... } // Default to hybrid
    }
}
```

This design allows experimentation with different algorithms while maintaining a consistent interface.

---

## Appendix: Performance Estimates

### Connection Count Comparison

| Cluster Size | Star Topology | Hybrid Topology | Reduction |
|--------------|---------------|-----------------|-----------|
| 10 nodes     | 90 total      | 40 total        | 55%       |
| 50 nodes     | 2,450 total   | 200 total       | 92%       |
| 100 nodes    | 9,900 total   | 400 total       | 96%       |
| 500 nodes    | 249,500 total | 2,000 total     | 99.2%     |
| 1000 nodes   | 999,000 total | 4,000 total     | 99.6%     |

*(Hybrid assumes cohortSize=4, bridgeCount=2)*

### Latency Estimates

| Cluster Size | Avg Hops | Latency @ 1ms/hop | Latency @ 5ms/hop |
|--------------|----------|-------------------|-------------------|
| 10 nodes     | 1.5      | 1.5ms             | 7.5ms             |
| 50 nodes     | 2.3      | 2.3ms             | 11.5ms            |
| 100 nodes    | 2.8      | 2.8ms             | 14ms              |
| 500 nodes    | 3.5      | 3.5ms             | 17.5ms            |
| 1000 nodes   | 4.2      | 4.2ms             | 21ms              |

### Memory Savings

**Per connection overhead:**
- TCP socket: ~4KB kernel buffers
- Read/write goroutines: ~8KB stack each
- Application buffers: configurable (default 1024 slots)

**Example with 100 nodes:**

| Topology | Connections/Node | Mem/Node | Total Cluster Mem |
|----------|------------------|----------|-------------------|
| Star     | 99               | ~2MB     | 200MB             |
| Hybrid   | 5                | ~100KB   | 10MB              |

**95% memory reduction!**

---

## Conclusion

The hybrid ring-star topology provides a scalable path forward for Mycorrizal to support hundreds or thousands of nodes while maintaining bounded resource usage per node. The design leverages existing rendezvous hashing infrastructure, requires no central coordination, and can be implemented incrementally with full backward compatibility.

**Next steps:**
1. Review this design document
2. Get stakeholder approval
3. Begin Phase 1 implementation
4. Iterate based on testing results

**Success metrics:**
- ✅ Support 1000+ node clusters
- ✅ ≤ 6 connections per node regardless of cluster size
- ✅ < 20ms routing latency for 1000 nodes
- ✅ Zero downtime upgrades from star topology
- ✅ Existing application code unchanged