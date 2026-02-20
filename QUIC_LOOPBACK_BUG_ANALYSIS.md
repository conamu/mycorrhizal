# QUIC Loopback Connection Bug - Root Cause Analysis

## Problem Summary

Nodes in a 3-node cluster are connecting to themselves instead of to remote nodes, causing cache operations to fail with "self-connection not allowed" errors.

## Root Cause (CONFIRMED)

From the logs:

```
time=2025-12-28T09:54:26.885+01:00 level=ERROR msg="rejecting incoming self-connection"
nodeID=78eba62b-494e-4b56-9f40-c87f433b0afa
localNodeID=78eba62b-494e-4b56-9f40-c87f433b0afa
remoteAddr=127.0.0.1:7083
localAddr=127.0.0.1:7083
```

**Key observation**: `remoteAddr == localAddr` - The node is literally connecting to its own QUIC listener!

## Technical Details

### Current Architecture

Each node has:
- **Memberlist port** (for gossip protocol): 7070, 6969, 7171
- **QUIC port** (for data communication): 7083, 6982, 7184

### The Bug

In `/Users/constantinamundsen/go/github.com/conamu/mycorrizal/internal/nodosum/memberlist.go` (line ~46):

```go
func (d Delegate) NotifyJoin(node *memberlist.Node) {
    // ...
    addr, err := net.ResolveUDPAddr("udp", node.Addr.String()+":"+strconv.Itoa(d.quicPort))
    // ...
}
```

**The problem**: `d.quicPort` is the DELEGATE's (local node's) QUIC port, NOT the target node's QUIC port!

### Example

When Node A (QUIC port 7083) tries to connect to Node B:

1. `node.Addr.String()` = "127.0.0.1" (Node B's IP from memberlist)
2. `d.quicPort` = 7083 (Node A's own QUIC port - WRONG!)
3. Result: `"127.0.0.1:7083"` (Node A's own address)

Node A dials its own QUIC listener, creating a loopback connection!

### Why It Partially Works

- Some connections succeed initially (you can see successful connections in logs)
- But then nodes attempt to dial their own listeners for subsequent operations
- The self-connection check in `handleQuicConn()` correctly rejects these (working as intended)
- Cache operations fail because they can't establish streams to "remote" nodes that are actually self

## The Solution

### Recommended: Store QUIC Port in Memberlist Metadata (Option 1)

Each node should advertise its QUIC port in memberlist metadata so other nodes can dial the correct port.

#### Implementation

**File 1**: `/Users/constantinamundsen/go/github.com/conamu/mycorrizal/internal/nodosum/nodosum.go`

In the `New()` function, set metadata when creating memberlist config:

```go
// Set node metadata to include QUIC port
cfg.MemberlistConfig.Metadata = []byte(fmt.Sprintf("quicPort=%d", cfg.QuicPort))
```

**File 2**: `/Users/constantinamundsen/go/github.com/conamu/mycorrizal/internal/nodosum/memberlist.go`

Add helper function to parse metadata:

```go
func parseQuicPortFromMetadata(meta []byte) (int, error) {
    // Parse "quicPort=7083" format
    str := string(meta)
    if !strings.HasPrefix(str, "quicPort=") {
        return 0, fmt.Errorf("invalid metadata format: %s", str)
    }
    portStr := strings.TrimPrefix(str, "quicPort=")
    port, err := strconv.Atoi(portStr)
    if err != nil {
        return 0, err
    }
    return port, nil
}
```

In `NotifyJoin()` function (around line 46), replace:

```go
// OLD (WRONG - uses local QUIC port):
addr, err := net.ResolveUDPAddr("udp", node.Addr.String()+":"+strconv.Itoa(d.quicPort))

// NEW (CORRECT - parse target node's QUIC port from metadata):
targetQuicPort, err := parseQuicPortFromMetadata(node.Meta)
if err != nil {
    d.logger.Error("failed to parse QUIC port from node metadata", "node", node.Name, "error", err)
    return
}
addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", node.Addr.String(), targetQuicPort))
```

### Alternative Solutions

**Option 2: Use Fixed Port Mapping**

If QUIC ports follow a predictable pattern:

```go
// If QUIC port = memberlist port + fixed offset
quicPort := node.Port + 1000
addr := fmt.Sprintf("%s:%d", node.Addr.String(), quicPort)
```

**Option 3: Configure QUIC Ports Explicitly**

Maintain a configuration map of `nodeID -> QUIC address`.

## Testing Plan

After implementing the fix:

1. Start 3 nodes on localhost with different QUIC ports
2. Verify NO "rejecting incoming self-connection" errors in logs
3. Verify cache Set/Get operations succeed across nodes
4. Check connection logs to confirm each node connects to OTHER nodes' QUIC ports
5. Monitor that `remoteAddr != localAddr` for all connections

## Impact

- **Files Modified**: 2
  - `internal/nodosum/nodosum.go` (add metadata setting)
  - `internal/nodosum/memberlist.go` (read metadata, add helper)
- **Breaking Changes**: None (backward compatible if old nodes ignore metadata)
- **Performance Impact**: Negligible (metadata parsing once per connection)

## Notes

- The self-connection check in `handleQuicConn()` (added in conn.go:73-84) is working correctly and should remain
- The real bug is in address resolution when dialing QUIC connections
- This explains why the target node ID was correct in debugging but connections still failed
- The issue only manifests when multiple nodes run on the same host (localhost testing)
