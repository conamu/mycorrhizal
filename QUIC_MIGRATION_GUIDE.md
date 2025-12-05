# QUIC Stream Migration Guide

This guide walks through completing the migration from TCP-based custom multiplexing to QUIC native stream multiplexing.

## Current Status

### ✅ Part 1: Critical Bug Fixes - **COMPLETED**

All critical bugs have been fixed:
- ✅ Nil pointer panic in `conn.go` accept loop fixed (lines 38-46) - now has `continue` after error
- ✅ Defensive nil check added in `handleQuicConn()` (lines 54-58)
- ✅ Missing RUnlock fixed in `memberlist.go` NotifyJoin (lines 26-32)
- ✅ Nil pointer panic after Dial error fixed (lines 42-44) - now has `return`
- ✅ BONUS: Panic recovery added to NotifyJoin (lines 19-24)

**No more shutdown panics!** The system now handles connection errors gracefully.

---

## Table of Contents

1. [~~Critical Bug Fixes~~](#part-1-critical-bug-fixes---completed) ✅ **DONE**
2. [Complete QUIC Stream Implementation](#part-2-complete-quic-stream-implementation) ⚠️ **TODO**
3. [Refactor Application Layer](#part-3-refactor-application-layer-for-quic) ⚠️ **TODO**
4. [Remove Legacy TCP Code](#part-4-remove-legacy-tcp-code) ⚠️ **TODO**
5. [Testing and Validation](#part-5-testing-and-validation)
6. [Request-Response Patterns](#part-6-request-response-patterns) ⚠️ **TODO** (HIGH PRIORITY)

---

## Part 1: Critical Bug Fixes - ✅ COMPLETED

### Summary of Fixes Applied

#### ✅ Fix 1: Accept Loop Error Handling
**File:** `internal/nodosum/conn.go:38-46`

Fixed code now includes proper error handling:
```go
conn, err := ln.Accept(n.ctx)
if err != nil {
    // Check if this is a shutdown error
    if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
        n.logger.Debug("quic listener shutting down")
        return
    }
    n.logger.Error(fmt.Sprintf("error accepting quic connection: %s", err.Error()))
    continue  // ✅ FIXED - skips calling handler with nil conn
}
go n.handleQuicConn(conn)
```

#### ✅ Fix 2: Nil Check in handleQuicConn
**File:** `internal/nodosum/conn.go:54-58`

```go
func (n *Nodosum) handleQuicConn(conn *quic.Conn) {
    if conn == nil {  // ✅ FIXED - defensive nil check
        n.logger.Error("handleQuicConn called with nil connection")
        return
    }
    nodeID := conn.ConnectionState().TLS.ServerName
```

#### ✅ Fix 3: Proper Lock Management in NotifyJoin
**File:** `internal/nodosum/memberlist.go:26-32`

```go
d.quicConns.RLock()
if _, exists := d.quicConns.conns[node.Name]; exists {
    d.quicConns.RUnlock()
    d.logger.Debug(fmt.Sprintf("quic connection exists: %s", node.Name))
    return
}
d.quicConns.RUnlock()  // ✅ FIXED - always unlocks before Dial
```

#### ✅ Fix 4: Early Return After Dial Error
**File:** `internal/nodosum/memberlist.go:42-44`

```go
conn, err := d.quicTransport.Dial(dialCtx, addr, d.tlsConfig, d.quicConfig)
if err != nil {
    d.logger.Error(fmt.Sprintf("error accepting quic connection: %s", err.Error()))
    return  // ✅ FIXED - exits early instead of using nil conn
}
```

#### ✅ Bonus: Panic Recovery
**File:** `internal/nodosum/memberlist.go:19-24`

```go
func (d Delegate) NotifyJoin(node *memberlist.Node) {
    defer func() {  // ✅ BONUS - catches any panics
        if r := recover(); r != nil {
            d.logger.Error("panic in NotifyJoin recovered", "panic", r, "node", node.Name)
            debug.PrintStack()
        }
    }()
```

**Result:** System no longer panics on shutdown or connection errors! 🎉

---

## Part 2: Complete QUIC Stream Implementation ⚠️ TODO

### Current Problem: Streams Are Registered But Never Read

**File:** `internal/nodosum/conn.go:87-112`
**Function:** `handleStream()`

**Current code:**
```go
func (n *Nodosum) handleStream(stream *quic.Stream) {
    if stream == nil {
        return
    }

    frameType := make([]byte, 1)
    _, err := io.ReadFull(stream, frameType)
    if err != nil {
        n.logger.Error(fmt.Sprintf("error reading quic frame type: %s", err.Error()))
    }

    nodeId, appId, streamName, err := decodeStreamInit(stream)
    if err != nil {
        n.logger.Error(fmt.Sprintf("error decoding quic initial frame: %s", err.Error()))
    }

    key := fmt.Sprintf("%s:%s:%s", nodeId, appId, streamName)
    n.quicApplicationStreams.Lock()
    n.quicApplicationStreams.streams[key] = stream
    n.quicApplicationStreams.Unlock()

    n.logger.Debug(fmt.Sprintf("Registered stream, %s, %s, %s", nodeId, appId, streamName))

    // ❌ PROBLEM: FUNCTION ENDS HERE - Nobody reads from this stream!
}
```

The stream is registered in the map but never actually used for reading data.

---

### Solution: Add Stream Read Loop

Replace the entire `handleStream()` function with this implementation:

```go
func (n *Nodosum) handleStream(stream quic.Stream) {
    if stream == nil {
        n.logger.Error("handleStream called with nil stream")
        return
    }

    // Read frame type (should be STREAM_INIT)
    frameTypeBuf := make([]byte, 1)
    _, err := io.ReadFull(stream, frameTypeBuf)
    if err != nil {
        n.logger.Error("failed to read frame type from stream", "error", err)
        stream.Close()
        return
    }

    frameType := frameTypeBuf[0]
    if frameType != FRAME_TYPE_STREAM_INIT {
        n.logger.Error("expected STREAM_INIT frame as first frame", "got", frameType)
        stream.Close()
        return
    }

    // Decode stream initialization
    nodeId, appId, streamName, err := decodeStreamInit(stream)
    if err != nil {
        n.logger.Error("failed to decode stream init", "error", err)
        stream.Close()
        return
    }

    n.logger.Debug("stream initialized", "nodeId", nodeId, "app", appId, "name", streamName)

    // Register stream in registry
    key := fmt.Sprintf("%s:%s:%s", nodeId, appId, streamName)
    n.quicApplicationStreams.Lock()
    n.quicApplicationStreams.streams[key] = &stream
    n.quicApplicationStreams.Unlock()

    // Get the application handler
    val, ok := n.applications.Load(appId)
    if !ok {
        n.logger.Warn("no application registered for stream", "app", appId)
        stream.Close()
        return
    }
    application := val.(*application)

    // ✅ NEW: Start read loop for this stream
    n.wg.Add(1)
    go func() {
        defer n.wg.Done()
        defer stream.Close()
        defer func() {
            // Clean up stream from registry on exit
            n.quicApplicationStreams.Lock()
            delete(n.quicApplicationStreams.streams, key)
            n.quicApplicationStreams.Unlock()
            n.logger.Debug("stream closed and cleaned up", "key", key)
        }()

        n.logger.Debug("starting read loop for stream", "key", key)

        for {
            // Check context cancellation
            select {
            case <-n.ctx.Done():
                return
            default:
            }

            // Read frame type
            frameTypeBuf := make([]byte, 1)
            _, err := io.ReadFull(stream, frameTypeBuf)
            if err != nil {
                if err == io.EOF {
                    n.logger.Debug("stream closed by peer", "key", key)
                    return
                }
                n.logger.Error("failed to read frame type", "error", err, "key", key)
                return
            }

            frameType := frameTypeBuf[0]

            switch frameType {
            case FRAME_TYPE_DATA:
                // Read length (4 bytes)
                lengthBuf := make([]byte, 4)
                _, err := io.ReadFull(stream, lengthBuf)
                if err != nil {
                    n.logger.Error("failed to read data length", "error", err)
                    return
                }
                length := binary.BigEndian.Uint32(lengthBuf)

                // Read payload
                payload := make([]byte, length)
                _, err = io.ReadFull(stream, payload)
                if err != nil {
                    n.logger.Error("failed to read payload", "error", err)
                    return
                }

                // Route to application receive worker
                select {
                case application.receiveWorker.InputChan <- payload:
                    // Successfully sent to application
                case <-n.ctx.Done():
                    return
                }

            default:
                n.logger.Warn("unknown frame type on stream", "frameType", frameType, "key", key)
                return
            }
        }
    }()
}
```

**Required Import:**
```go
import (
    "encoding/binary"
    // ... other imports
)
```

**Key Changes:**
1. ✅ Added read loop that continuously reads from the stream
2. ✅ Routes DATA frames directly to application receive worker
3. ✅ Proper cleanup on stream close (removes from registry)
4. ✅ Context cancellation support for graceful shutdown
5. ✅ Error handling for EOF and other errors
6. ✅ WaitGroup tracking for coordinated shutdown

---

### Add Stream Cleanup on Connection Close

When a QUIC connection closes, all its streams should be cleaned up.

**Location:** Add this new function to `internal/nodosum/conn.go` or `nodosum.go`:

```go
func (n *Nodosum) closeQuicConnection(nodeId string) {
    // Close the QUIC connection
    n.quicConns.Lock()
    if conn, exists := n.quicConns.conns[nodeId]; exists {
        if conn != nil {
            conn.CloseWithError(0, "connection closed")
        }
        delete(n.quicConns.conns, nodeId)
    }
    n.quicConns.Unlock()

    // Clean up all streams for this node
    n.quicApplicationStreams.Lock()
    keysToDelete := make([]string, 0)
    for key := range n.quicApplicationStreams.streams {
        // Key format: "nodeId:app:name"
        if strings.HasPrefix(key, nodeId+":") {
            keysToDelete = append(keysToDelete, key)
        }
    }
    for _, key := range keysToDelete {
        if stream := n.quicApplicationStreams.streams[key]; stream != nil {
            (*stream).Close()
        }
        delete(n.quicApplicationStreams.streams, key)
    }
    n.quicApplicationStreams.Unlock()

    n.logger.Debug("closed QUIC connection and all streams", "nodeId", nodeId, "streamsClosed", len(keysToDelete))
}
```

**Update NotifyLeave:** Use this function in `memberlist.go:69-85`:

```go
func (d Delegate) NotifyLeave(node *memberlist.Node) {
    d.closeQuicConnection(node.Name)  // ✅ Use the new cleanup function
    d.logger.Debug(fmt.Sprintf("node left: %s", node.Name))
}
```

---

## Part 3: Refactor Application Layer for QUIC ⚠️ TODO

This section removes the TCP-era multiplexer and makes applications use QUIC streams directly.

### Current Architecture (TCP-based)

```
Application.Send(data)
    ↓
sendWorker.InputChan
    ↓
globalWriteChannel (shared by all apps)
    ↓
multiplexerTaskOutbound (N workers)
    ↓ [adds frame header with ApplicationID]
    ↓
nodeConn.writeChan (per TCP connection)
    ↓
TCP connection (single stream)
```

### Target Architecture (QUIC-based)

```
Application.Send(data)
    ↓
getOrCreateQuicStream(nodeId, appId, "data")
    ↓
stream.Write(encodeDataFrame(data))
    ↓
QUIC connection (multiple streams, one per app)
```

---

### Step 1: Add Helper Functions

**File:** Create or add to `internal/nodosum/quic.go`

```go
package nodosum

import (
    "encoding/binary"
    "fmt"

    "github.com/quic-go/quic-go"
)

// getConnectedNodeIds returns list of all connected node IDs
func (n *Nodosum) getConnectedNodeIds() []string {
    nodeIds := make([]string, 0)

    n.quicConns.RLock()
    for nodeId := range n.quicConns.conns {
        nodeIds = append(nodeIds, nodeId)
    }
    n.quicConns.RUnlock()

    return nodeIds
}

// encodeDataFrame creates a QUIC data frame: [TYPE:1][LENGTH:4][PAYLOAD:N]
func encodeDataFrame(payload []byte) []byte {
    frame := make([]byte, 1+4+len(payload))
    frame[0] = FRAME_TYPE_DATA
    binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
    copy(frame[5:], payload)
    return frame
}
```

---

### Step 2: Refactor Application.Send()

**File:** `internal/nodosum/application.go`
**Lines:** 74-89

**Replace the current `Send()` function:**

```go
func (a *application) Send(payload []byte, ids []string) error {
    // Determine target nodes
    targetNodes := ids
    if len(targetNodes) == 0 {
        // Broadcast: get all connected nodes
        targetNodes = a.nodosum.getConnectedNodeIds()
    }

    if len(targetNodes) == 0 {
        return fmt.Errorf("no connected nodes to send to")
    }

    // Send to each target node via dedicated QUIC stream
    var sendErrors []error
    for _, nodeId := range targetNodes {
        // Get or create stream for this app to this node
        stream, err := a.nodosum.getOrCreateQuicStream(nodeId, a.id, "data")
        if err != nil {
            a.nodosum.logger.Error("failed to get stream", "error", err, "nodeId", nodeId, "app", a.id)
            sendErrors = append(sendErrors, fmt.Errorf("node %s: %w", nodeId, err))
            continue
        }

        // Encode and write data frame to stream
        frame := encodeDataFrame(payload)
        _, err = (*stream).Write(frame)
        if err != nil {
            a.nodosum.logger.Error("failed to write to stream", "error", err, "nodeId", nodeId, "app", a.id)
            sendErrors = append(sendErrors, fmt.Errorf("node %s: %w", nodeId, err))
            continue
        }
    }

    if len(sendErrors) > 0 {
        return fmt.Errorf("failed to send to %d/%d nodes: %v", len(sendErrors), len(targetNodes), sendErrors)
    }

    return nil
}
```

**Key Changes:**
- ✅ Removed dependency on `sendWorker`
- ✅ Writes directly to QUIC streams
- ✅ Gets stream per node using `getOrCreateQuicStream()`
- ✅ Handles broadcast by getting all connected node IDs
- ✅ Better error handling with per-node error tracking

---

### Step 3: Remove Send Worker from Application Struct

**File:** `internal/nodosum/application.go`
**Lines:** 19-25

**Update the `application` struct:**

```go
type application struct {
    id            string
    nodosum       *Nodosum           // ✅ ADDED - need reference to Nodosum
    receiveFunc   func(payload []byte) error
    nodes         []string
    // sendWorker    *worker.Worker  // ❌ REMOVE - no longer needed
    receiveWorker *worker.Worker     // ✅ KEEP - still needed for receiving
}
```

---

### Step 4: Update RegisterApplication()

**File:** `internal/nodosum/application.go`
**Lines:** 33-64

**Update the function to remove send worker creation:**

```go
func (n *Nodosum) RegisterApplication(uniqueIdentifier string) Application {
    // ❌ REMOVE: sendWorker creation
    // sendWorker := worker.NewWorker(n.ctx, fmt.Sprintf("%s-send", uniqueIdentifier), n.wg, n.applicationSendTask, n.logger, 0)
    // sendWorker.InputChan = make(chan any, 100)
    // sendWorker.OutputChan = n.globalWriteChannel
    // go sendWorker.Start()

    // ✅ KEEP: receiveWorker for incoming messages
    receiveWorker := worker.NewWorker(n.ctx, fmt.Sprintf("%s-receive", uniqueIdentifier), n.wg, n.applicationReceiveTask, n.logger, 0)
    receiveWorker.InputChan = make(chan any, 100)
    go receiveWorker.Start()

    nodes := []string{}

    n.nodeMeta.Lock()
    for _, meta := range n.nodeMeta.Map {
        if !meta.alive {
            continue
        }
        nodes = append(nodes, meta.ID)
    }
    n.nodeMeta.Unlock()

    app := &application{
        id:            uniqueIdentifier,
        nodosum:       n,              // ✅ ADDED - store reference
        // sendWorker:    sendWorker,   // ❌ REMOVED
        receiveWorker: receiveWorker,  // ✅ KEEP
        nodes:         nodes,
    }

    n.applications.Store(uniqueIdentifier, app)
    return app
}
```

---

### Step 5: Remove applicationSendTask

**File:** `internal/nodosum/application.go`
**Lines:** 105-107

**Delete this function entirely:**

```go
// ❌ DELETE THIS ENTIRE FUNCTION:
// func (n *Nodosum) applicationSendTask(w *worker.Worker, msg any) {
//     w.OutputChan <- msg
// }
```

It's no longer needed since applications write directly to streams.

---

## Part 4: Remove Legacy TCP Code ⚠️ TODO

Once applications send directly via QUIC streams, the old multiplexer infrastructure can be removed.

### Step 1: Remove Global Channels

**File:** `internal/nodosum/nodosum.go`
**Lines:** 59-62

**Remove these fields from the `Nodosum` struct:**

```go
type Nodosum struct {
    // ... keep other fields ...

    // ❌ DELETE these lines:
    // globalReadChannel chan any
    // globalWriteChannel chan any

    // ... keep other fields ...
}
```

**Update `New()` function (lines 161-162):**

Remove channel initialization:
```go
func New(cfg *Config) (*Nodosum, error) {
    // ... existing code ...

    n := &Nodosum{
        // ... other fields ...

        // ❌ DELETE these lines:
        // globalReadChannel:      make(chan any, cfg.MultiplexerBufferSize),
        // globalWriteChannel:     make(chan any, cfg.MultiplexerBufferSize),

        // ... other fields ...
    }

    // ... rest of function ...
}
```

---

### Step 2: Remove Multiplexer Start Call

**File:** `internal/nodosum/nodosum.go`
**Function:** `Start()` (around line 197)

The `Start()` function doesn't currently call `startMultiplexer()`, so verify it's not being called anywhere:

```bash
# Search for any calls to startMultiplexer
grep -r "startMultiplexer" internal/nodosum/
```

If found, remove those calls.

---

### Step 3: Delete mux.go Entirely

**File:** `internal/nodosum/mux.go`

This entire file is no longer needed. Delete it:

```bash
rm internal/nodosum/mux.go
```

Or if you want to keep it for reference, rename it:

```bash
mv internal/nodosum/mux.go internal/nodosum/mux.go.deprecated
```

**This file contained:**
- `startMultiplexer()` - Started inbound/outbound multiplexer workers
- `multiplexerTaskInbound()` - Routed incoming frames by ApplicationID
- `multiplexerTaskOutbound()` - Added frame headers and routed to connections

All of this functionality is replaced by QUIC streams with the stream init protocol.

---

### Step 4: Remove Multiplexer Config Fields

**File:** `config.go` (if these exist)

Remove any multiplexer-related configuration:

```go
type Config struct {
    // ... keep other fields ...

    // ❌ REMOVE if present:
    // MultiplexerBufferSize  int
    // MultiplexerWorkerCount int
}
```

---

### Step 5: Clean Up TCP Frame Protocol (Optional)

**File:** `internal/nodosum/protocol.go`

The TCP-specific frame encoding with ApplicationID headers can be simplified.

**Keep:**
- QUIC frame types: `FRAME_TYPE_STREAM_INIT`, `FRAME_TYPE_DATA`
- `encodeStreamInit()`, `decodeStreamInit()`
- `encodeDataFrame()` (added in Part 3)

**Consider deprecating (but may still be needed for TCP connections if kept):**
- `encodeFrameHeader()` with ApplicationID
- `decodeFrameHeader()`
- TCP frame types: `SYSTEM`, `APP`, `CONN_INIT`, etc.

**Decision point:** If you're keeping TCP support for backwards compatibility, keep these. If fully migrating to QUIC only, they can be removed.

---

### Step 6: Update Node Tracking

**File:** `internal/nodosum/application.go`
**Field:** `nodes []string` in `application` struct (line 22)

This field may need to be updated dynamically. Consider:

```go
func (a *application) Nodes() []string {
    // Return live nodes from Nodosum instead of static list
    return a.nodosum.getConnectedNodeIds()
}
```

This ensures the application always has current node list for broadcasting.

---

## Part 5: Testing and Validation

### Quick Validation Steps

After implementing Parts 2-4, verify:

#### 1. Check Streams Are Created
Add temporary debug logging in `getOrCreateQuicStream()`:
```go
n.logger.Debug("stream operation",
    "nodeId", nodeId,
    "app", app,
    "name", name,
    "action", action) // "created" or "retrieved"
```

Run your application and verify you see "stream created" logs.

#### 2. Check Streams Are Read From
The new `handleStream()` read loop should log:
```go
n.logger.Debug("starting read loop for stream", "key", key)
```

Verify this appears in logs for each application stream.

#### 3. Check Messages Are Received
Test with SYSTEM-MYCEL or custom application:
```go
app := mycorrizal.RegisterApplication("test-app")
app.SetReceiveFunc(func(payload []byte) error {
    fmt.Printf("Received: %s\n", string(payload))
    return nil
})
```

Send a message and verify it's received.

#### 4. Check No Panics on Shutdown
```go
node.Start()
time.Sleep(2 * time.Second)
node.Shutdown()  // Should NOT panic
```

Check logs for "panic" - there should be none.

#### 5. Check Stream Cleanup
After shutdown, all streams should be closed and removed from registry. Add debug logging to verify:
```go
n.logger.Debug("stream registry size", "count", len(n.quicApplicationStreams.streams))
```

Should be 0 after shutdown.

---

### Test Scenarios

#### Test 1: Basic Point-to-Point
```go
// Create two nodes
node1, _ := mycorrizal.New(config1)
node2, _ := mycorrizal.New(config2)

node1.Start()
node2.Start()
time.Sleep(2 * time.Second)

// Register application on both nodes
app1 := node1.RegisterApplication("test")
app2 := node2.RegisterApplication("test")

received := make(chan string, 1)
app2.SetReceiveFunc(func(payload []byte) error {
    received <- string(payload)
    return nil
})

// Send from node1 to node2
app1.Send([]byte("hello"), []string{node2.Id()})

// Verify received
select {
case msg := <-received:
    fmt.Printf("✅ Received: %s\n", msg)
case <-time.After(5 * time.Second):
    fmt.Println("❌ Timeout waiting for message")
}

node1.Shutdown()
node2.Shutdown()
```

#### Test 2: Broadcast
```go
// Create 3 nodes
nodes := []*mycorrizal.Mycorrizal{node1, node2, node3}

for _, node := range nodes {
    node.Start()
}
time.Sleep(2 * time.Second)

received := make(chan string, 3)

// Register on all nodes
for _, node := range nodes {
    app := node.RegisterApplication("broadcast-test")
    app.SetReceiveFunc(func(payload []byte) error {
        received <- string(payload)
        return nil
    })
}

// Broadcast from node1
app1 := node1.GetApplication("broadcast-test")
app1.Send([]byte("broadcast"), []string{})  // Empty = broadcast

// Should receive on node2 and node3 (not node1)
for i := 0; i < 2; i++ {
    select {
    case msg := <-received:
        fmt.Printf("✅ Node received: %s\n", msg)
    case <-time.After(5 * time.Second):
        fmt.Printf("❌ Timeout on message %d\n", i+1)
    }
}
```

#### Test 3: Multiple Applications
```go
// Test independent streams for different applications
cache := node1.RegisterApplication("SYSTEM-MYCEL")
events := node1.RegisterApplication("SYSTEM-CYTOPLASM")

// Send on both - should not interfere
cache.Send([]byte("cache data"), []string{})
events.Send([]byte("event data"), []string{})

// Both should arrive independently
```

#### Test 4: Connection Loss Recovery
```go
node1.Start()
node2.Start()
time.Sleep(2 * time.Second)

app1 := node1.RegisterApplication("test")
app1.Send([]byte("msg1"), []string{node2.Id()})

// Force connection close
node2.Shutdown()
time.Sleep(1 * time.Second)

// Restart node2
node2, _ = mycorrizal.New(config2)
node2.Start()
time.Sleep(2 * time.Second)

// Should reconnect and work
app1.Send([]byte("msg2"), []string{node2.Id()})
```

---

### Common Issues and Solutions

#### Issue: "no application registered for stream"

**Cause:** Stream received before application registered.

**Solution:** Ensure applications are registered immediately after `Start()`:
```go
node.Start()
app := node.RegisterApplication("my-app")  // Do this right away
```

#### Issue: "failed to get stream" errors

**Cause:** Node not connected yet, or connection closed.

**Solution:**
1. Check memberlist connection: `n.ml.Members()` should include target node
2. Check QUIC connection: `n.quicConns` should have entry for node
3. Add retry logic or queue messages until connected

#### Issue: Messages not received

**Debugging steps:**
1. Check sender sees "stream created" or "retrieved" log
2. Check receiver sees "stream initialized" log
3. Check receiver sees "starting read loop for stream" log
4. Check sender writes complete without error
5. Check receiver's application callback is set: `app.SetReceiveFunc(...)`

#### Issue: High memory usage

**Cause:** Streams not cleaned up properly.

**Solution:**
1. Verify `defer stream.Close()` in read loop
2. Verify stream cleanup in `closeQuicConnection()`
3. Monitor stream registry size: `len(n.quicApplicationStreams.streams)`

---

### Performance Validation

After migration, verify performance improvements:

#### 1. No Head-of-Line Blocking
Send large message on one application while sending small messages on another:
```go
// Large message on cache
cache.Send(make([]byte, 10*1024*1024), []string{})

// Small messages on events should not be delayed
for i := 0; i < 100; i++ {
    events.Send([]byte("small"), []string{})
}
```

With QUIC streams, events should complete quickly despite large cache transfer.

#### 2. Independent Flow Control
Each application's stream has independent flow control. Slow receiver on one app shouldn't affect others.

#### 3. Concurrent Sends
Multiple goroutines sending should work without blocking:
```go
var wg sync.WaitGroup
for i := 0; i < 100; i++ {
    wg.Add(1)
    go func(id int) {
        defer wg.Done()
        app.Send([]byte(fmt.Sprintf("msg-%d", id)), []string{})
    }(i)
}
wg.Wait()
```

Should complete quickly without contention.

---

## Migration Checklist

Use this to track your progress:

### Part 1: Critical Bug Fixes
- [x] Fix nil pointer panic in accept loop (conn.go:38-46)
- [x] Add nil check in handleQuicConn (conn.go:54-58)
- [x] Fix missing RUnlock in NotifyJoin (memberlist.go:26-32)
- [x] Fix nil pointer after Dial error (memberlist.go:42-44)
- [x] Bonus: Add panic recovery (memberlist.go:19-24)

### Part 2: Complete QUIC Stream Implementation
- [ ] Add read loop in handleStream() (conn.go:87-112)
- [ ] Add stream cleanup function closeQuicConnection()
- [ ] Update NotifyLeave to use cleanup function
- [ ] Test that streams are read from
- [ ] Test that streams are cleaned up on disconnect

### Part 3: Refactor Application Layer
- [ ] Add helper functions: getConnectedNodeIds(), encodeDataFrame()
- [ ] Refactor Application.Send() to write directly to streams
- [ ] Remove sendWorker from application struct
- [ ] Update RegisterApplication() to remove sendWorker creation
- [ ] Delete applicationSendTask function
- [ ] Add nodosum reference to application struct
- [ ] Test end-to-end message sending

### Part 4: Remove Legacy Code
- [ ] Remove globalReadChannel from Nodosum struct
- [ ] Remove globalWriteChannel from Nodosum struct
- [ ] Remove channel initialization in New()
- [ ] Delete mux.go file (or rename to .deprecated)
- [ ] Remove multiplexer config fields
- [ ] Update application.Nodes() to return live nodes
- [ ] Clean up protocol.go (optional)

### Part 5: Testing
- [ ] Test basic point-to-point messaging
- [ ] Test broadcast messaging
- [ ] Test multiple applications independently
- [ ] Test connection loss and recovery
- [ ] Test graceful shutdown (no panics)
- [ ] Test stream cleanup
- [ ] Performance validation (no head-of-line blocking)
- [ ] Load test with concurrent sends

### Part 6: Request-Response Patterns (HIGH PRIORITY)
- [ ] Add FRAME_TYPE_REQUEST and FRAME_TYPE_RESPONSE constants
- [ ] Implement request/response frame encoding/decoding
- [ ] Add Request() method to Application interface
- [ ] Add SetRequestHandler() method to Application interface
- [ ] Implement correlation tracking with pendingRequests map
- [ ] Implement timeout mechanism
- [ ] Add handleIncomingFrame() dispatcher for frame types
- [ ] Fix Mycel getRemote() to use Request()
- [ ] Add Mycel cache request handler
- [ ] Test request-response with concurrent requests (100+)
- [ ] Test timeout handling
- [ ] Validate < 5ms p99 latency

---

## Part 6: Request-Response Patterns ⚠️ TODO (HIGH PRIORITY)

### Problem Statement

The current Application interface only supports **one-way message passing**:

```go
type Application interface {
    Send(payload []byte, ids []string) error        // Fire and forget
    SetReceiveFunc(func(payload []byte) error)      // Just receives
    Nodes() []string
}
```

**Critical issue**: No way to correlate responses with requests!

This is why Mycel's `getRemote()` is broken (`internal/mycel/remote.go`):
```go
func (c *cache) getRemote(bucket, key string) (any, error) {
    // ...
    err := c.app.Send(nil, []string{target})  // ❌ Just sends, doesn't wait for response
    return target, nil  // ❌ Returns node ID instead of actual cached value!
}
```

**Impact**: Distributed cache operations don't work across nodes.

---

### Recommended Solution: Correlation IDs

Add request-response semantics on top of existing long-lived QUIC streams using correlation tracking.

**Why this approach**:
- ✅ Works with existing stream architecture
- ✅ Backward compatible (Send/Receive unchanged)
- ✅ Low overhead (~0.1ms, 200 bytes/request)
- ✅ Perfect for high-frequency operations (cache)
- ✅ Simple implementation (~300 lines)

**Alternative considered**: Stream-per-request (QUIC native)
- ❌ Rejected: Too much overhead for cache (1ms stream creation per GET)
- ✅ Better for low-frequency RPC operations

---

### API Changes

#### Enhanced Application Interface

**File:** `internal/nodosum/application.go`

```go
type Application interface {
    // V1 API (existing - unchanged)
    Send(payload []byte, ids []string) error
    SetReceiveFunc(func(payload []byte) error)
    Nodes() []string

    // V2 API (new - request-response)
    Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error)
    SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error))
}

var (
    ErrRequestTimeout = errors.New("request timeout")
    ErrNoHandler      = errors.New("no request handler registered")
)
```

**Usage example**:
```go
// Client side - send request, wait for response
app := mycorrizal.RegisterApplication("my-app")
response, err := app.Request(requestData, "node-123", 5*time.Second)
if err != nil {
    if errors.Is(err, ErrRequestTimeout) {
        // Handle timeout
    }
    return err
}
processResponse(response)

// Server side - handle requests
app.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
    result := processRequest(payload)
    return result, nil  // Automatically sent back to requester
})
```

---

### Protocol Changes

#### New Frame Types

**File:** `internal/nodosum/protocol.go`

Add two new frame types:

```go
const (
    FRAME_TYPE_STREAM_INIT = 0x01  // Existing
    FRAME_TYPE_DATA        = 0x02  // Existing
    FRAME_TYPE_REQUEST     = 0x03  // NEW
    FRAME_TYPE_RESPONSE    = 0x04  // NEW
)
```

#### Frame Formats

**REQUEST frame**:
```
[1 byte]  Frame type (0x03)
[2 bytes] Correlation ID length
[N bytes] Correlation ID (UUID string)
[4 bytes] Payload length
[N bytes] Payload
```

**RESPONSE frame**:
```
[1 byte]  Frame type (0x04)
[2 bytes] Correlation ID length
[N bytes] Correlation ID (matches request)
[1 byte]  Success flag (1=success, 0=error)
[4 bytes] Payload length
[N bytes] Payload (data if success, error message if failure)
```

#### Frame Encoding/Decoding

**Add to protocol.go**:

```go
func encodeRequestFrame(correlationID string, payload []byte) []byte {
    corrIDBytes := []byte(correlationID)
    corrIDLen := uint16(len(corrIDBytes))
    payloadLen := uint32(len(payload))

    buf := make([]byte, 0, 1+2+len(corrIDBytes)+4+len(payload))
    buf = append(buf, FRAME_TYPE_REQUEST)
    buf = binary.BigEndian.AppendUint16(buf, corrIDLen)
    buf = append(buf, corrIDBytes...)
    buf = binary.BigEndian.AppendUint32(buf, payloadLen)
    buf = append(buf, payload...)
    return buf
}

func decodeRequestFrame(data []byte) (correlationID string, payload []byte, err error) {
    if len(data) < 7 {
        return "", nil, errors.New("request frame too short")
    }
    if data[0] != FRAME_TYPE_REQUEST {
        return "", nil, errors.New("not a request frame")
    }

    offset := 1
    corrIDLen := binary.BigEndian.Uint16(data[offset:])
    offset += 2

    correlationID = string(data[offset : offset+int(corrIDLen)])
    offset += int(corrIDLen)

    payloadLen := binary.BigEndian.Uint32(data[offset:])
    offset += 4

    payload = data[offset : offset+int(payloadLen)]
    return correlationID, payload, nil
}

func encodeResponseFrame(correlationID string, success bool, payload []byte) []byte {
    corrIDBytes := []byte(correlationID)
    corrIDLen := uint16(len(corrIDBytes))
    payloadLen := uint32(len(payload))

    buf := make([]byte, 0, 1+2+len(corrIDBytes)+1+4+len(payload))
    buf = append(buf, FRAME_TYPE_RESPONSE)
    buf = binary.BigEndian.AppendUint16(buf, corrIDLen)
    buf = append(buf, corrIDBytes...)

    if success {
        buf = append(buf, 1)
    } else {
        buf = append(buf, 0)
    }

    buf = binary.BigEndian.AppendUint32(buf, payloadLen)
    buf = append(buf, payload...)
    return buf
}

func decodeResponseFrame(data []byte) (correlationID string, success bool, payload []byte, err error) {
    if len(data) < 8 {
        return "", false, nil, errors.New("response frame too short")
    }
    if data[0] != FRAME_TYPE_RESPONSE {
        return "", false, nil, errors.New("not a response frame")
    }

    offset := 1
    corrIDLen := binary.BigEndian.Uint16(data[offset:])
    offset += 2

    correlationID = string(data[offset : offset+int(corrIDLen)])
    offset += int(corrIDLen)

    success = data[offset] == 1
    offset++

    payloadLen := binary.BigEndian.Uint32(data[offset:])
    offset += 4

    payload = data[offset : offset+int(payloadLen)]
    return correlationID, success, payload, nil
}
```

**Required import**:
```go
import (
    "encoding/binary"
    "github.com/google/uuid"  // For generating correlation IDs
    // ... existing imports
)
```

---

### Implementation: Application Struct Changes

**File:** `internal/nodosum/application.go`

#### Update application struct

```go
type application struct {
    id              string
    nodosum         *Nodosum  // NEW - need reference for routing
    receiveFunc     func(payload []byte) error
    requestHandler  func(payload []byte, fromNode string) ([]byte, error)  // NEW
    nodes           []string
    sendWorker      *worker.Worker
    receiveWorker   *worker.Worker
    pendingRequests *sync.Map  // NEW - map[correlationID]*pendingRequest
}

type pendingRequest struct {
    correlationID string
    responseChan  chan *requestResponse
    timeout       *time.Timer
    sentAt        time.Time
}

type requestResponse struct {
    payload []byte
    err     error
}
```

#### Implement Request() method

```go
func (a *application) Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error) {
    // Generate correlation ID
    correlationID := uuid.New().String()

    // Create response channel and timeout
    responseChan := make(chan *requestResponse, 1)
    timer := time.NewTimer(timeout)

    // Register pending request
    pr := &pendingRequest{
        correlationID: correlationID,
        responseChan:  responseChan,
        timeout:       timer,
        sentAt:        time.Now(),
    }
    a.pendingRequests.Store(correlationID, pr)

    // Cleanup on exit
    defer func() {
        a.pendingRequests.Delete(correlationID)
        timer.Stop()
    }()

    // Encode and send request frame
    frame := encodeRequestFrame(correlationID, payload)
    err := a.Send(frame, []string{targetNode})
    if err != nil {
        return nil, fmt.Errorf("failed to send request: %w", err)
    }

    // Wait for response or timeout
    select {
    case resp := <-responseChan:
        if resp.err != nil {
            return nil, resp.err
        }
        return resp.payload, nil

    case <-timer.C:
        return nil, ErrRequestTimeout

    case <-a.receiveWorker.Ctx.Done():
        return nil, errors.New("application shutting down")
    }
}
```

#### Implement SetRequestHandler() method

```go
func (a *application) SetRequestHandler(handler func(payload []byte, fromNode string) ([]byte, error)) {
    a.requestHandler = handler
}
```

#### Update receive worker to handle new frame types

Modify the existing `applicationReceiveTask` or add new frame dispatcher:

```go
// Called by receive worker for each incoming frame
func (a *application) handleIncomingFrame(frame []byte, fromNode string) error {
    if len(frame) < 1 {
        return errors.New("frame too short")
    }

    frameType := frame[0]

    switch frameType {
    case FRAME_TYPE_REQUEST:
        return a.handleRequest(frame, fromNode)

    case FRAME_TYPE_RESPONSE:
        return a.handleResponse(frame, fromNode)

    case FRAME_TYPE_DATA:
        // Existing one-way message handling
        if a.receiveFunc != nil {
            return a.receiveFunc(frame[1:])
        }

    default:
        return fmt.Errorf("unknown frame type: %d", frameType)
    }

    return nil
}

func (a *application) handleRequest(frame []byte, fromNode string) error {
    correlationID, payload, err := decodeRequestFrame(frame)
    if err != nil {
        return err
    }

    // Check if handler registered
    if a.requestHandler == nil {
        // Send error response
        errFrame := encodeResponseFrame(correlationID, false, []byte(ErrNoHandler.Error()))
        return a.Send(errFrame, []string{fromNode})
    }

    // Invoke handler
    responsePayload, err := a.requestHandler(payload, fromNode)

    // Send response
    var respFrame []byte
    if err != nil {
        respFrame = encodeResponseFrame(correlationID, false, []byte(err.Error()))
    } else {
        respFrame = encodeResponseFrame(correlationID, true, responsePayload)
    }

    return a.Send(respFrame, []string{fromNode})
}

func (a *application) handleResponse(frame []byte, fromNode string) error {
    correlationID, success, payload, err := decodeResponseFrame(frame)
    if err != nil {
        return err
    }

    // Find pending request
    val, ok := a.pendingRequests.Load(correlationID)
    if !ok {
        // Response for unknown request (probably timed out)
        return nil
    }

    pr := val.(*pendingRequest)

    // Prepare response
    resp := &requestResponse{
        payload: payload,
    }
    if !success {
        resp.err = errors.New(string(payload))
    }

    // Send to waiting goroutine (non-blocking)
    select {
    case pr.responseChan <- resp:
        // Delivered
    default:
        // Channel full or closed (shouldn't happen)
    }

    return nil
}
```

#### Update RegisterApplication()

**Modify initialization** in `nodosum.go`:

```go
func (n *Nodosum) RegisterApplication(uniqueIdentifier string) Application {
    // ... existing worker creation ...

    app := &application{
        id:              uniqueIdentifier,
        nodosum:         n,                     // NEW - add reference
        receiveFunc:     nil,
        requestHandler:  nil,                   // NEW - initialize
        nodes:           nodes,
        sendWorker:      sendWorker,
        receiveWorker:   receiveWorker,
        pendingRequests: &sync.Map{},          // NEW - initialize
    }

    // Update receive worker to call handleIncomingFrame
    receiveWorker.TaskFunc = func(w *worker.Worker, msg any) {
        payload := msg.([]byte)
        // Assume fromNode is encoded in metadata (needs implementation)
        err := app.handleIncomingFrame(payload, "unknown")  // TODO: get actual fromNode
        if err != nil {
            w.Logger.Error("frame handling error", "err", err.Error())
        }
    }

    n.applications.Store(uniqueIdentifier, app)
    return app
}
```

**NOTE**: You'll need to pass `fromNode` information with each frame. Options:
1. Add to frame header (preferred)
2. Store in context/metadata
3. Look up from connection registry

---

### Mycel Integration

#### Fix getRemote() Implementation

**File:** `internal/mycel/remote.go`

```go
func (c *cache) getRemote(bucket, key string) (any, error) {
    // Find which node has this key
    c.nodeScoreHashMap.RLock()
    target := c.nodeScoreHashMap.data[bucket+key]
    c.nodeScoreHashMap.RUnlock()

    if target == "" {
        return nil, errors.New("no target node for key")
    }

    // Create cache GET request
    req := &CacheOperation{
        Type:   OP_GET,
        Bucket: bucket,
        Key:    key,
    }
    requestPayload, err := encodeCacheOp(req)
    if err != nil {
        return nil, fmt.Errorf("failed to encode request: %w", err)
    }

    // Send request and wait for response (5 second timeout)
    responsePayload, err := c.app.Request(requestPayload, target, 5*time.Second)
    if err != nil {
        if errors.Is(err, ErrRequestTimeout) {
            return nil, fmt.Errorf("remote get timeout for key %s", key)
        }
        return nil, fmt.Errorf("remote get failed: %w", err)
    }

    // Decode response
    resp, err := decodeCacheOp(responsePayload)
    if err != nil {
        return nil, fmt.Errorf("failed to decode response: %w", err)
    }

    if resp.Type == OP_GET_RESPONSE {
        return resp.Value, nil
    }

    return nil, errors.New("key not found on remote node")
}
```

#### Add Cache Request Handler

**File:** `internal/mycel/mycel.go` (in Start() method)

```go
func (m *mycel) Start() error {
    // ... existing code ...

    m.app = m.ndsm.RegisterApplication("SYSTEM-MYCEL")

    // Set one-way message handler (existing)
    m.app.SetReceiveFunc(m.cache.applicationReceiveTask)

    // Set request-response handler (NEW)
    m.app.SetRequestHandler(m.cache.handleCacheRequest)

    // ... rest of start ...
}
```

**Add handler function** in `internal/mycel/cache.go`:

```go
func (c *cache) handleCacheRequest(payload []byte, fromNode string) ([]byte, error) {
    // Decode cache operation
    op, err := decodeCacheOp(payload)
    if err != nil {
        return nil, fmt.Errorf("invalid cache operation: %w", err)
    }

    switch op.Type {
    case OP_GET:
        // Look up value locally
        val, err := c.getLocal(op.Bucket, op.Key)
        if err != nil {
            return nil, fmt.Errorf("key not found: %w", err)
        }

        // Create response
        resp := &CacheOperation{
            Type:   OP_GET_RESPONSE,
            Bucket: op.Bucket,
            Key:    op.Key,
            Value:  val,
        }
        return encodeCacheOp(resp)

    case OP_PUT:
        // Handle remote PUT request
        err := c.Put(op.Bucket, op.Key, op.Value, op.TTL)
        if err != nil {
            return nil, err
        }

        // Return success response
        resp := &CacheOperation{
            Type: OP_PUT_RESPONSE,
        }
        return encodeCacheOp(resp)

    case OP_DELETE:
        // Handle remote DELETE request
        err := c.Delete(op.Bucket, op.Key)
        if err != nil {
            return nil, err
        }

        resp := &CacheOperation{
            Type: OP_DELETE_RESPONSE,
        }
        return encodeCacheOp(resp)

    default:
        return nil, fmt.Errorf("unknown operation type: %d", op.Type)
    }
}
```

#### Define Cache Operations

**Add to `internal/mycel/cache.go`**:

```go
const (
    OP_GET            = 0x01
    OP_GET_RESPONSE   = 0x02
    OP_PUT            = 0x03
    OP_PUT_RESPONSE   = 0x04
    OP_DELETE         = 0x05
    OP_DELETE_RESPONSE = 0x06
)

type CacheOperation struct {
    Type   byte
    Bucket string
    Key    string
    Value  any
    TTL    time.Duration
}

func encodeCacheOp(op *CacheOperation) ([]byte, error) {
    // Use gob or protobuf encoding
    var buf bytes.Buffer
    enc := gob.NewEncoder(&buf)
    if err := enc.Encode(op); err != nil {
        return nil, err
    }
    return buf.Bytes(), nil
}

func decodeCacheOp(data []byte) (*CacheOperation, error) {
    var op CacheOperation
    buf := bytes.NewBuffer(data)
    dec := gob.NewDecoder(buf)
    if err := dec.Decode(&op); err != nil {
        return nil, err
    }
    return &op, nil
}
```

---

### Testing Strategy

#### Unit Tests

**File:** `internal/nodosum/application_test.go`

```go
func TestRequestResponse(t *testing.T) {
    // Setup two test nodes
    node1 := setupTestNode("node-1")
    node2 := setupTestNode("node-2")
    defer node1.Shutdown()
    defer node2.Shutdown()

    // Register applications
    app1 := node1.RegisterApplication("test-app")
    app2 := node2.RegisterApplication("test-app")

    // Set handler on node2
    app2.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
        // Echo back with prefix
        return append([]byte("echo: "), payload...), nil
    })

    // Send request from node1 to node2
    response, err := app1.Request([]byte("hello"), "node-2", 5*time.Second)

    assert.NoError(t, err)
    assert.Equal(t, []byte("echo: hello"), response)
}

func TestRequestTimeout(t *testing.T) {
    node1 := setupTestNode("node-1")
    node2 := setupTestNode("node-2")
    defer node1.Shutdown()
    defer node2.Shutdown()

    app1 := node1.RegisterApplication("test-app")
    app2 := node2.RegisterApplication("test-app")

    // Handler that sleeps longer than timeout
    app2.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
        time.Sleep(10 * time.Second)
        return payload, nil
    })

    // Request with short timeout
    start := time.Now()
    _, err := app1.Request([]byte("test"), "node-2", 1*time.Second)
    elapsed := time.Since(start)

    assert.ErrorIs(t, err, ErrRequestTimeout)
    assert.Less(t, elapsed, 2*time.Second)  // Should timeout around 1s
}

func TestConcurrentRequests(t *testing.T) {
    node1 := setupTestNode("node-1")
    node2 := setupTestNode("node-2")
    defer node1.Shutdown()
    defer node2.Shutdown()

    app1 := node1.RegisterApplication("test-app")
    app2 := node2.RegisterApplication("test-app")

    // Simple echo handler
    app2.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
        return payload, nil
    })

    // Send 100 concurrent requests
    var wg sync.WaitGroup
    errors := make(chan error, 100)

    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()

            payload := []byte(fmt.Sprintf("request-%d", i))
            resp, err := app1.Request(payload, "node-2", 5*time.Second)

            if err != nil {
                errors <- err
                return
            }

            if !bytes.Equal(resp, payload) {
                errors <- fmt.Errorf("response mismatch for request-%d", i)
            }
        }(i)
    }

    wg.Wait()
    close(errors)

    // All requests should succeed
    for err := range errors {
        t.Errorf("request failed: %v", err)
    }
}

func TestRequestHandlerError(t *testing.T) {
    node1 := setupTestNode("node-1")
    node2 := setupTestNode("node-2")
    defer node1.Shutdown()
    defer node2.Shutdown()

    app1 := node1.RegisterApplication("test-app")
    app2 := node2.RegisterApplication("test-app")

    // Handler that returns error
    app2.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
        return nil, errors.New("handler error")
    })

    _, err := app1.Request([]byte("test"), "node-2", 5*time.Second)

    assert.Error(t, err)
    assert.Contains(t, err.Error(), "handler error")
}

func TestNoRequestHandler(t *testing.T) {
    node1 := setupTestNode("node-1")
    node2 := setupTestNode("node-2")
    defer node1.Shutdown()
    defer node2.Shutdown()

    app1 := node1.RegisterApplication("test-app")
    _ = node2.RegisterApplication("test-app")  // No handler set

    _, err := app1.Request([]byte("test"), "node-2", 5*time.Second)

    assert.Error(t, err)
    assert.Contains(t, err.Error(), "no request handler")
}
```

#### Integration Tests for Mycel

**File:** `internal/mycel/mycel_test.go`

```go
func TestMycelGetRemote(t *testing.T) {
    // Setup 3-node cluster
    cluster := setupTestCluster(3)
    defer cluster.Shutdown()

    // Create cache bucket on all nodes
    for _, node := range cluster.Nodes {
        cache := node.Mycel.Cache()
        err := cache.CreateBucket("test", 10*time.Minute, 1000)
        assert.NoError(t, err)
    }

    // Put value on node1
    cache1 := cluster.Nodes[0].Mycel.Cache()
    err := cache1.Put("test", "key1", "value1", 0)
    assert.NoError(t, err)

    // Wait for cluster sync
    time.Sleep(100 * time.Millisecond)

    // Get from node2 (should fetch from node1 via request-response)
    cache2 := cluster.Nodes[1].Mycel.Cache()
    val, err := cache2.Get("test", "key1")

    assert.NoError(t, err)
    assert.Equal(t, "value1", val)
}

func TestMycelRemoteTimeout(t *testing.T) {
    cluster := setupTestCluster(2)
    defer cluster.Shutdown()

    cache1 := cluster.Nodes[0].Mycel.Cache()
    cache2 := cluster.Nodes[1].Mycel.Cache()

    cache1.CreateBucket("test", 10*time.Minute, 1000)
    cache2.CreateBucket("test", 10*time.Minute, 1000)

    // Shutdown node2
    cluster.Nodes[1].Shutdown()

    // Try to get key that's on node2
    _, err := cache1.Get("test", "nonexistent-key")

    assert.Error(t, err)
    // Should timeout since node2 is down
}
```

---

### Performance Targets

| Metric | Target | Measurement Method |
|--------|--------|-------------------|
| **Request Latency (LAN)** | < 5ms p99 | Histogram in tests |
| **Timeout Accuracy** | ±100ms | Measure actual vs expected |
| **Memory per Request** | < 500 bytes | Runtime memory profiling |
| **Max Concurrent Requests** | 1,000+ | Load test without errors |
| **Goroutine Leaks** | 0 | Check goroutine count before/after |
| **Success Rate** | > 99.9% | Under normal conditions |

**Benchmark example**:

```go
func BenchmarkRequestResponse(b *testing.B) {
    node1 := setupTestNode("node-1")
    node2 := setupTestNode("node-2")
    defer node1.Shutdown()
    defer node2.Shutdown()

    app1 := node1.RegisterApplication("bench")
    app2 := node2.RegisterApplication("bench")

    app2.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
        return payload, nil
    })

    payload := []byte("benchmark")

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := app1.Request(payload, "node-2", 5*time.Second)
        if err != nil {
            b.Fatal(err)
        }
    }
}
```

---

### Implementation Checklist

#### Part 6.1: Protocol Foundation
- [ ] Add FRAME_TYPE_REQUEST (0x03) constant
- [ ] Add FRAME_TYPE_RESPONSE (0x04) constant
- [ ] Implement encodeRequestFrame()
- [ ] Implement decodeRequestFrame()
- [ ] Implement encodeResponseFrame()
- [ ] Implement decodeResponseFrame()
- [ ] Add uuid package dependency
- [ ] Test frame encoding/decoding roundtrip

#### Part 6.2: Application Interface
- [ ] Add Request() method signature to Application interface
- [ ] Add SetRequestHandler() method signature
- [ ] Define ErrRequestTimeout error
- [ ] Define ErrNoHandler error
- [ ] Add pendingRequests field to application struct
- [ ] Add requestHandler field to application struct
- [ ] Add nodosum reference to application struct
- [ ] Define pendingRequest struct
- [ ] Define requestResponse struct

#### Part 6.3: Request Implementation
- [ ] Implement Request() method with correlation ID generation
- [ ] Implement timeout mechanism with timer
- [ ] Implement pending request registration
- [ ] Implement cleanup on function exit
- [ ] Implement context cancellation handling
- [ ] Test Request() with successful response
- [ ] Test Request() with timeout
- [ ] Test Request() with handler error

#### Part 6.4: Response Handling
- [ ] Implement SetRequestHandler() method
- [ ] Implement handleIncomingFrame() dispatcher
- [ ] Implement handleRequest() method
- [ ] Implement handleResponse() method
- [ ] Handle missing request handler case
- [ ] Handle unknown correlation ID case
- [ ] Test request handling end-to-end
- [ ] Test concurrent requests (100+)

#### Part 6.5: Mycel Integration
- [ ] Define CacheOperation struct
- [ ] Define cache operation constants (OP_GET, etc.)
- [ ] Implement encodeCacheOp()
- [ ] Implement decodeCacheOp()
- [ ] Fix getRemote() to use Request()
- [ ] Implement handleCacheRequest() handler
- [ ] Register handler in Mycel.Start()
- [ ] Test cache GET across nodes
- [ ] Test cache PUT across nodes
- [ ] Test cache DELETE across nodes

#### Part 6.6: Testing & Validation
- [ ] Write TestRequestResponse unit test
- [ ] Write TestRequestTimeout unit test
- [ ] Write TestConcurrentRequests (100+ requests)
- [ ] Write TestRequestHandlerError unit test
- [ ] Write TestNoRequestHandler unit test
- [ ] Write TestMycelGetRemote integration test
- [ ] Write BenchmarkRequestResponse
- [ ] Validate < 5ms p99 latency
- [ ] Validate no goroutine leaks
- [ ] Validate no memory leaks with pprof

---

## Estimated Timeline

- **Part 2:** 2-3 hours (stream read loop + cleanup)
- **Part 3:** 2-4 hours (application refactor)
- **Part 4:** 1-2 hours (remove legacy code)
- **Part 5:** 2-3 hours (testing and validation)
- **Part 6:** 1-2 weeks (request-response implementation)
  - **Part 6.1-6.2:** 2-3 days (protocol + interface)
  - **Part 6.3-6.4:** 2-3 days (request/response logic)
  - **Part 6.5:** 1-2 days (Mycel integration)
  - **Part 6.6:** 2-3 days (testing & validation)

**Total: 7-12 hours for Parts 2-4, plus 1-2 weeks for Part 6**

**Recommendation**: Complete Part 6 FIRST (it's higher priority for Mycel functionality), then do Parts 2-4 as optimization.

---

## Getting Help

If you encounter issues:

1. **Check logs** - Look for errors in QUIC connection/stream handling
2. **Use race detector** - `go test -race` catches concurrency bugs
3. **Add debug logging** - Trace messages through the system
4. **Test incrementally** - Don't make all changes at once
5. **Keep git commits small** - Easy to revert if something breaks

Good luck with completing the migration! The end result will be cleaner, faster, and more reliable. 🚀
