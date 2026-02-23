# Memberlist Delegate Interface Documentation

This document explains the `memberlist.Delegate` interface and how Mycorrizal implements it.

## Overview

The Delegate interface is how memberlist (Hashicorp's gossip protocol library) allows you to hook into the cluster membership system. It provides 5 methods that handle:
- Metadata broadcasting
- Custom message handling
- State synchronization

## Interface Methods

### 1. NodeMeta(limit int) []byte

**Purpose**: Returns metadata about THIS node that will be gossiped to all other nodes in the cluster.

**When called**:
- During node startup
- When memberlist needs to broadcast your node's information
- Periodically during gossip rounds

**Parameters**:
- `limit` - Maximum size in bytes (typically 512 bytes)

**What it should return**:
- Arbitrary byte slice containing metadata about your node
- Must be <= limit bytes
- This data gets gossiped to ALL nodes in the cluster
- Other nodes receive it in `memberlist.Node.Meta`

**Mycorrizal Implementation**:
```go
func (d Delegate) NodeMeta(limit int) []byte {
    // Encode QUIC port as 2 bytes (uint16 can hold ports 0-65535)
    portBytes := make([]byte, 2)
    binary.BigEndian.PutUint16(portBytes, uint16(d.quicPort))
    return portBytes
}
```

**Why we need this**: We advertise our QUIC listener port so other nodes know which port to dial when establishing QUIC connections to us.

**Reading metadata from other nodes**:
```go
func (d Delegate) NotifyJoin(node *memberlist.Node) {
    // node.Meta contains the remote node's metadata (from their NodeMeta())
    targetQuicPort := binary.BigEndian.Uint16(node.Meta[:2])
    // Now we know which port to dial on the remote node
}
```

---

### 2. NotifyMsg(bytes []byte)

**Purpose**: Called when a user-defined message is received via UDP.

**When called**:
- When another node sends a custom message using `memberlist.SendToUDP()` or `SendToTCP()`
- NOT for regular gossip protocol messages (memberlist handles those internally)

**Parameters**:
- `bytes` - The raw message payload sent by the remote node

**What it should do**:
- Deserialize and handle the custom message
- This is for application-level messages that use memberlist's UDP transport

**Mycorrizal Implementation**:
```go
func (d Delegate) NotifyMsg(bytes []byte) {
    // Not used - all application data goes over QUIC
    // We use memberlist only for membership/discovery
    // QUIC handles all application-level messaging
}
```

**When to use**: If you want to send lightweight messages via memberlist's UDP transport instead of establishing separate connections.

**Why we don't use it**: Mycorrizal uses QUIC for all application data transfer, so we don't need memberlist's message passing.

---

### 3. GetBroadcasts(overhead, limit int) [][]byte

**Purpose**: Returns messages to be broadcast to the cluster during the next gossip round.

**When called**:
- Periodically during gossip rounds
- Memberlist calls this to see if you have any messages to piggyback on gossip packets

**Parameters**:
- `overhead` - Bytes already consumed by memberlist protocol headers
- `limit` - Maximum total bytes you can return (includes overhead)

**What it should return**:
- Slice of byte slices, each representing a message to broadcast
- Total size of all messages should be <= (limit - overhead)
- Messages get included in gossip packets and propagate through the cluster

**Mycorrizal Implementation**:
```go
func (d Delegate) GetBroadcasts(overhead, limit int) [][]byte {
    return nil  // Not using memberlist broadcasts
}
```

**When to use**: For disseminating small updates cluster-wide (e.g., "cache invalidation for key X", "service health status changed").

**Why we don't use it**: We handle application-level broadcasts via QUIC streams, not memberlist gossip.

---

### 4. LocalState(join bool) []byte

**Purpose**: Returns a snapshot of local state to send to a node during state synchronization.

**When called**:
- During node joins (when `join = true`)
- During periodic anti-entropy sync (when `join = false`)
- When memberlist initiates a full-state transfer to another node

**Parameters**:
- `join` - True if this is for a new node joining, false for periodic sync

**What it should return**:
- Serialized representation of your node's current state
- The remote node receives this in `MergeRemoteState()`
- Used for anti-entropy: ensuring all nodes eventually have consistent state

**Mycorrizal Implementation**:
```go
func (d Delegate) LocalState(join bool) []byte {
    return nil  // No additional state beyond what memberlist tracks
}
```

**When to use**: When you have application state that needs to be synchronized across nodes (e.g., distributed cache metadata, service registry).

**Why we don't use it**: Memberlist already tracks node membership (which nodes are alive/dead). We don't have additional shared state that needs anti-entropy sync.

---

### 5. MergeRemoteState(buf []byte, join bool)

**Purpose**: Receives state from another node and merges it with local state.

**When called**:
- When receiving state from `LocalState()` of another node
- During state synchronization and anti-entropy repairs

**Parameters**:
- `buf` - Serialized state from the remote node (from their `LocalState()`)
- `join` - True if remote node is joining, false for periodic sync

**What it should do**:
- Deserialize the remote state
- Merge it with your local state using your conflict resolution logic
- Update local view to include remote node's information

**Mycorrizal Implementation**:
```go
func (d Delegate) MergeRemoteState(buf []byte, join bool) {
    // No state to merge - memberlist handles membership automatically
}
```

**When to use**: Paired with `LocalState()` - if you return state there, you must handle merging it here.

**Why we don't use it**: Since we return `nil` from `LocalState()`, there's nothing to merge.

---

## Implementation Summary

### Current Mycorrizal Usage

| Method | Status | Purpose in Mycorrizal |
|--------|--------|----------------------|
| `NodeMeta()` | ✅ **IMPLEMENTED** | Advertises QUIC listener port |
| `NotifyMsg()` | ⚪ No-op | Don't use memberlist messaging |
| `GetBroadcasts()` | ⚪ No-op | Don't broadcast via gossip |
| `LocalState()` | ⚪ No-op | No shared state to sync |
| `MergeRemoteState()` | ⚪ No-op | No remote state to merge |

### Why This Design?

Mycorrizal uses a **separation of concerns** approach:

- **Memberlist**: Node discovery and failure detection only
  - Tracks which nodes exist in the cluster
  - Detects when nodes join/leave/fail
  - Gossips basic node information (IP, ports, alive/dead status)

- **QUIC**: All application data transfer
  - Cache operations (get/set/delete)
  - Application messaging
  - Request/response patterns

This keeps the architecture clean: memberlist is lightweight gossip for membership, QUIC is high-performance transport for data.

### Future Enhancements

These methods could be useful for:

1. **NotifyMsg/GetBroadcasts**:
   - Cache invalidation notifications
   - Cluster-wide event broadcasts
   - Lightweight coordination messages

2. **LocalState/MergeRemoteState**:
   - Synchronizing cache metadata across nodes
   - Service discovery registry state
   - Consistent configuration propagation

## Common Patterns

### Pattern 1: Metadata for Connection Information

```go
// What Mycorrizal does
func (d Delegate) NodeMeta(limit int) []byte {
    portBytes := make([]byte, 2)
    binary.BigEndian.PutUint16(portBytes, uint16(d.quicPort))
    return portBytes
}
```

**Use case**: Each node advertises ports, capabilities, or connection details.

### Pattern 2: Event Broadcasting

```go
// Example (not currently used)
func (d Delegate) GetBroadcasts(overhead, limit int) [][]byte {
    broadcasts := [][]byte{}

    // Get pending events to broadcast
    for _, event := range d.pendingEvents {
        if len(broadcasts) + len(event) <= (limit - overhead) {
            broadcasts = append(broadcasts, event)
        }
    }

    return broadcasts
}
```

**Use case**: Efficiently disseminate small updates cluster-wide.

### Pattern 3: State Synchronization

```go
// Example (not currently used)
func (d Delegate) LocalState(join bool) []byte {
    // Serialize local cache metadata
    return json.Marshal(d.cacheMetadata)
}

func (d Delegate) MergeRemoteState(buf []byte, join bool) {
    var remoteMeta CacheMetadata
    json.Unmarshal(buf, &remoteMeta)

    // Merge with local metadata (last-write-wins, vector clocks, etc.)
    d.cacheMetadata.Merge(remoteMeta)
}
```

**Use case**: Anti-entropy for distributed state that needs eventual consistency.

## References

- [Memberlist GitHub](https://github.com/hashicorp/memberlist)
- [Memberlist Delegate Interface](https://pkg.go.dev/github.com/hashicorp/memberlist#Delegate)
- Mycorrizal implementation: `/Users/constantinamundsen/go/github.com/conamu/mycorrizal/internal/nodosum/memberlist.go`
