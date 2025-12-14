# QUIC Migration Analysis & Fix Plan

Comprehensive analysis of the QUIC migration covering both clustering issues discovered during 3-node testing and fundamental architectural gaps in the stream implementation.

## Current State Summary

### What Works ✅
- QUIC connection establishment via memberlist gossip
- Duplicate connection prevention with proper locking
- TLS-based authentication using 1Password CA certificates
- Stream creation and registration
- Basic frame encoding/decoding (STREAM_INIT, DATA frames)
- One-way Send() infrastructure (partial - send works, receive doesn't)
- Graceful shutdown with context cancellation
- **Connection close detection** - `isQuicConnectionClosed()` helper prevents error loops ✅
- **Panic recovery** in NotifyJoin() prevents memberlist callback crashes ✅
- **Dial timeout** with 5-second context prevents hanging ✅
- **Early returns** on dial failures prevent nil connection panics ✅
- **sync.Once** prevents double Start() calls ✅
- **Defer** ensures readyChan always closes ✅
- Local cache operations fully functional ✅

### What's Broken ❌
- **CRITICAL**: `streamReadLoop()` has syntax error and references non-existent functions (conn.go:149-189)
  - Line 158: `frameType, frameLen, err := (stream)` - missing function call
  - References FRAME_TYPE_REQUEST/RESPONSE which don't exist in quic.go
  - Calls `n.routeToApplication()`, `n.handleRequest()`, `n.handleResponse()` which don't exist
- **CRITICAL**: `handleStream()` doesn't launch read loop - just registers stream and returns (conn.go:122-147)
- **CRITICAL**: Applications never receive messages - no mechanism to deliver them
- **CRITICAL**: Cache remote operations completely broken (remote.go:3-13)
  - Sends nil payload
  - Returns node ID instead of cached value
- **CRITICAL**: Cache receive task broken (task.go:50-57)
  - Uses non-existent `binary.Decode()` function
  - Empty error handling
- Request-response pattern not implemented
  - No REQUEST/RESPONSE frame types in quic.go
  - No Request() method on Application interface
  - No SetRequestHandler() method
- Race conditions in stream registry (2 known - registry.go)
- No timeout handling for operations
- No correlation system for matching requests/responses
- Named stream support not implemented (only "data" stream exists)
- Cache dto.go minimal (only GET defined, no PUT/DELETE/PUT_TTL)

### Migration Status: ~70% Complete

**What's been completed since last update:**
- ✅ Most clustering issues from Part 3 fixed
- ✅ Connection lifecycle management improved
- ✅ Error handling in memberlist callbacks

**Remaining critical work:**
1. Implement stream read/write loops (Phase 1) - **BLOCKS ALL FUNCTIONALITY**
2. Add request-response pattern with correlation IDs (Phase 2) - **NEEDED FOR CACHE**
3. Implement named stream support (Phase 3) - Optional enhancement
4. Fix cache layer serialization and operations (Phase 4) - **HIGH PRIORITY**
5. Fix remaining race conditions (Phase 5)
6. Comprehensive testing (Phase 7)

---

# FILE STRUCTURE REFERENCE

**Important**: The protocol implementation is in `quic.go`, not `protocol.go`

## Nodosum Files (internal/nodosum/)
- `quic.go` - Frame encoding/decoding, protocol constants (NOT protocol.go)
- `conn.go` - Connection and stream handling
- `application.go` - Application interface implementation
- `registry.go` - Stream registry
- `memberlist.go` - Memberlist delegate
- `nodosum.go` - Core orchestration
- `cert.go` - TLS certificate generation
- `config.go` - Configuration

## Mycel Files (internal/mycel/)
- `cache.go` - Public Cache interface
- `local.go` - Local cache operations
- `remote.go` - Remote cache operations (BROKEN)
- `task.go` - Worker tasks (PARTIALLY BROKEN)
- `dto.go` - Data transfer objects (MINIMAL)
- `ds.go` - Data structures (LRU, etc.)
- `mycel.go` - Component initialization
- `config.go` - Configuration
- **MISSING**: `protocol.go` - Needs to be created for cache protocol

---

# PART 1: CRITICAL ARCHITECTURAL GAPS

These issues prevent the library from functioning at all. Must be fixed before any features work.

---

## GAP 1: Stream Read/Write Loops Not Implemented

**Severity**: CRITICAL (blocks all functionality)

**Symptom**: Applications send messages but receivers never get them. Cache operations hang indefinitely.

**Location**: `internal/nodosum/conn.go:149-191`

**Root Cause**:
1. After streams are registered in `handleStream()` (line 122-147), the function returns immediately without launching a read loop
2. `streamReadLoop()` is **partially implemented** but has syntax errors and references non-existent functions
3. `streamWriteLoop()` is an empty stub

```go
// conn.go:149-189
func (n *Nodosum) streamReadLoop(stream quic.Stream, nodeID, appID string) {
    // ... has structure but BROKEN:
    frameType, frameLen, err := (stream)  // ❌ SYNTAX ERROR - missing function call
    // ... references FRAME_TYPE_REQUEST/RESPONSE which don't exist in quic.go
    // ... calls n.routeToApplication(), n.handleRequest(), n.handleResponse() which don't exist
}

func (n *Nodosum) streamWriteLoop() {}  // ❌ Empty stub (line 191)
```

**Current Data Flow (BROKEN):**
```
Node A → Node B:
1. A: Opens stream to B ✅
2. A: Sends STREAM_INIT frame ✅
3. A: Sends DATA frame ✅
4. B: Accepts stream ✅
5. B: Reads STREAM_INIT frame ✅
6. B: Registers stream ✅
7. B: handleStream() RETURNS ❌
8. B: DATA frame sits in stream buffer, never read ❌
9. Application never receives anything ❌
```

### Tasks

- [ ] **Fix `streamReadLoop()` syntax error and simplify** (conn.go:149-189)
  ```go
  func (n *Nodosum) streamReadLoop(stream quic.Stream, nodeID, appID string) {
      defer stream.Close()

      for {
          select {
          case <-n.ctx.Done():
              return
          default:
              // Read next frame using existing readFrame() function from quic.go
              frameType, payload, err := readFrame(stream)
              if err != nil {
                  if isQuicConnectionClosed(err) {
                      n.logger.Debug("stream closed", "nodeID", nodeID, "appID", appID)
                      return
                  }
                  n.logger.Error("error reading frame", "error", err)
                  return
              }

              // For now, only handle DATA frames (REQUEST/RESPONSE not yet implemented)
              switch frameType {
              case FRAME_TYPE_DATA:
                  n.routeToApplication(appID, payload, nodeID)
              default:
                  n.logger.Warn("unknown frame type", "type", frameType)
              }
          }
      }
  }
  ```

- [ ] **Update `handleStream()` to launch read loop** (conn.go:119-144)
  ```go
  func (n *Nodosum) handleStream(stream quic.Stream) {
      // Read stream init frame
      frameType, frameLen, err := readFrameHeader(stream)
      // ... existing init code ...

      // Register stream
      n.quicApplicationStreams.Lock()
      n.quicApplicationStreams.streams[key] = stream
      n.quicApplicationStreams.Unlock()

      // ✅ Launch read loop instead of returning
      go n.streamReadLoop(stream, streamInit.NodeID, streamInit.ApplicationID)
  }
  ```

- [ ] **Implement `routeToApplication()` to deliver messages**
  ```go
  func (n *Nodosum) routeToApplication(appID string, payload []byte, fromNode string) {
      n.applications.RLock()
      app, exists := n.applications.apps[appID]
      n.applications.RUnlock()

      if !exists {
          n.logger.Warn("received message for unknown application", "appID", appID)
          return
      }

      // Send to application's receive worker
      select {
      case app.receiveWorker.InputChan <- receiveMessage{
          payload: payload,
          fromNode: fromNode,
      }:
      case <-time.After(100 * time.Millisecond):
          n.logger.Warn("application receive channel full", "appID", appID)
      }
  }
  ```

- [ ] **Test one-way messaging works end-to-end**

---

## GAP 2: Request-Response Pattern Missing

**Severity**: CRITICAL (cache layer requires this)

**Symptom**: Cache `Get()` operations can't retrieve data from remote nodes. No way for applications to send requests and wait for responses.

**Location**: Multiple files - not implemented

**Root Cause**: The system only supports fire-and-forget messaging. There's no correlation ID system to match responses with requests.

### What's Needed

1. **New frame types** in quic.go (lines 9-12):
   - `FRAME_TYPE_REQUEST = 0x03` (includes correlation ID)
   - `FRAME_TYPE_RESPONSE = 0x04` (includes correlation ID)
   - **Note**: Currently only STREAM_INIT (0x01) and DATA (0x02) exist

2. **Correlation ID system**:
   - Use UUID for each request
   - pendingRequests map: correlationID → response channel
   - Mutex to protect map
   - Timeout handling (5 seconds default)

3. **Enhanced Application interface** in application.go:
   ```go
   type Application interface {
       Send(payload []byte, targetNodes []string) error
       Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error)  // NEW
       SetReceiveFunc(func([]byte, string) error)
       SetRequestHandler(func([]byte, string) ([]byte, error))  // NEW
       GetConnectedNodes() []string
   }
   ```

### Tasks

- [ ] **Add new frame types to quic.go** (after line 11)
  ```go
  const (
      FRAME_TYPE_STREAM_INIT = 0x01  // ✅ EXISTS
      FRAME_TYPE_DATA        = 0x02  // ✅ EXISTS
      FRAME_TYPE_REQUEST     = 0x03  // ❌ NEW - ADD THIS
      FRAME_TYPE_RESPONSE    = 0x04  // ❌ NEW - ADD THIS
  )

  type RequestFrame struct {
      CorrelationID string
      Payload       []byte
  }

  type ResponseFrame struct {
      CorrelationID string
      Payload       []byte
      Error         string  // Empty if success
  }
  ```

- [ ] **Add pendingRequests to Application struct** (application.go)
  ```go
  type application struct {
      // ... existing fields ...

      pendingRequests   map[string]chan *ResponseFrame
      pendingRequestsMu sync.RWMutex
      requestHandler    func([]byte, string) ([]byte, error)
  }
  ```

- [ ] **Implement Request() method** (application.go)
  ```go
  func (a *application) Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error) {
      if timeout == 0 {
          timeout = 5 * time.Second
      }

      // Generate correlation ID
      correlationID := uuid.New().String()

      // Create response channel
      responseChan := make(chan *ResponseFrame, 1)
      a.pendingRequestsMu.Lock()
      a.pendingRequests[correlationID] = responseChan
      a.pendingRequestsMu.Unlock()

      // Cleanup on return
      defer func() {
          a.pendingRequestsMu.Lock()
          delete(a.pendingRequests, correlationID)
          a.pendingRequestsMu.Unlock()
      }()

      // Send request frame
      reqFrame := RequestFrame{
          CorrelationID: correlationID,
          Payload:       payload,
      }
      err := a.sendRequestFrame(reqFrame, targetNode)
      if err != nil {
          return nil, fmt.Errorf("failed to send request: %w", err)
      }

      // Wait for response with timeout
      select {
      case resp := <-responseChan:
          if resp.Error != "" {
              return nil, fmt.Errorf("remote error: %s", resp.Error)
          }
          return resp.Payload, nil
      case <-time.After(timeout):
          return nil, fmt.Errorf("request timeout after %v", timeout)
      case <-a.ctx.Done():
          return nil, fmt.Errorf("context cancelled")
      }
  }
  ```

- [ ] **Implement SetRequestHandler() method** (application.go)
  ```go
  func (a *application) SetRequestHandler(handler func([]byte, string) ([]byte, error)) {
      a.requestHandler = handler
  }
  ```

- [ ] **Implement handleRequest() in nodosum** (conn.go)
  ```go
  func (n *Nodosum) handleRequest(stream quic.Stream, appID string, frameData []byte, fromNode string) {
      // Decode request frame
      var reqFrame RequestFrame
      err := decodeRequestFrame(frameData, &reqFrame)
      if err != nil {
          n.logger.Error("failed to decode request frame", "error", err)
          return
      }

      // Get application
      n.applications.RLock()
      app, exists := n.applications.apps[appID]
      n.applications.RUnlock()

      if !exists || app.requestHandler == nil {
          // Send error response
          respFrame := ResponseFrame{
              CorrelationID: reqFrame.CorrelationID,
              Error:         "application not found or no request handler",
          }
          sendResponseFrame(stream, respFrame)
          return
      }

      // Call handler
      respPayload, err := app.requestHandler(reqFrame.Payload, fromNode)

      // Send response
      respFrame := ResponseFrame{
          CorrelationID: reqFrame.CorrelationID,
          Payload:       respPayload,
      }
      if err != nil {
          respFrame.Error = err.Error()
      }

      sendResponseFrame(stream, respFrame)
  }
  ```

- [ ] **Implement handleResponse() in nodosum** (conn.go)
  ```go
  func (n *Nodosum) handleResponse(frameData []byte) {
      // Decode response frame
      var respFrame ResponseFrame
      err := decodeResponseFrame(frameData, &respFrame)
      if err != nil {
          n.logger.Error("failed to decode response frame", "error", err)
          return
      }

      // Find pending request
      // Note: This needs access to all applications' pending requests
      // Consider moving pendingRequests to Nodosum level
      n.applications.RLock()
      defer n.applications.RUnlock()

      for _, app := range n.applications.apps {
          app.pendingRequestsMu.RLock()
          responseChan, exists := app.pendingRequests[respFrame.CorrelationID]
          app.pendingRequestsMu.RUnlock()

          if exists {
              select {
              case responseChan <- &respFrame:
              default:
                  n.logger.Warn("response channel full", "correlationID", respFrame.CorrelationID)
              }
              return
          }
      }

      n.logger.Warn("received response for unknown request", "correlationID", respFrame.CorrelationID)
  }
  ```

- [ ] **Test request-response with timeout**
- [ ] **Test concurrent requests from same application**

---

## GAP 3: Named Stream Support Missing

**Severity**: HIGH (needed for performance and flexibility)

**Requirement**: Applications should have one default "data" stream for general communication, plus ability to create additional named streams (e.g., cache might use separate streams per bucket or operation type).

**Current State**: Only one "data" stream per app-node pair.

### Tasks

- [ ] **Update stream key format** (registry.go)
  ```go
  // Current: "{nodeID}:{appID}:data"
  // New: "{nodeID}:{appID}:{streamName}"
  // Default stream name: "default" (not "data")
  ```

- [ ] **Add OpenNamedStream() method** (application.go)
  ```go
  func (a *application) OpenNamedStream(streamName string, targetNode string) error {
      return a.nodosum.getOrOpenQuicStream(targetNode, a.uniqueIdentifier, streamName)
  }
  ```

- [ ] **Add SendOnStream() method** (application.go)
  ```go
  func (a *application) SendOnStream(streamName string, payload []byte, targetNode string) error {
      stream, err := a.nodosum.getQuicStream(targetNode, a.uniqueIdentifier, streamName)
      if err != nil {
          return fmt.Errorf("stream not found: %w", err)
      }

      return sendDataFrame(stream, payload)
  }
  ```

- [ ] **Update getOrOpenQuicStream() to use streamName parameter** (registry.go:25-60)

- [ ] **Update Send() to use default stream** (application.go:85-95)
  ```go
  func (a *application) Send(payload []byte, targetNodes []string) error {
      // Use "default" stream
      // ... existing code but with streamName="default"
  }
  ```

---

## GAP 4: Cache Layer Completely Broken

**Severity**: CRITICAL

**Location**: `internal/mycel/remote.go`, `internal/mycel/task.go`

**Issues**:
1. `getRemote()` sends nil payload and returns node ID instead of cached value
2. `applicationReceiveTask()` is incomplete stub
3. `binary.Decode()` doesn't exist in standard library
4. No cache request/response protocol defined

### Tasks

- [ ] **Create cache protocol** (NEW FILE: `internal/mycel/protocol.go`)
  ```go
  package mycel

  type CacheOperation uint8

  const (
      CACHE_OP_GET    CacheOperation = 0x01
      CACHE_OP_PUT    CacheOperation = 0x02
      CACHE_OP_DELETE CacheOperation = 0x03
      CACHE_OP_PUT_TTL CacheOperation = 0x04
  )

  type CacheRequest struct {
      Operation CacheOperation
      Bucket    string
      Key       string
      Value     []byte  // For PUT operations
      TTL       int64   // For PUT_TTL operations (duration in nanoseconds)
  }

  type CacheResponse struct {
      Success bool
      Value   []byte
      Error   string
  }

  func EncodeCacheRequest(req CacheRequest) ([]byte, error) {
      // Use encoding/gob or protobuf
  }

  func DecodeCacheRequest(data []byte) (CacheRequest, error) {
      // Use encoding/gob or protobuf
  }

  func EncodeCacheResponse(resp CacheResponse) ([]byte, error) {
      // Use encoding/gob or protobuf
  }

  func DecodeCacheResponse(data []byte) (CacheResponse, error) {
      // Use encoding/gob or protobuf
  }
  ```

- [ ] **Fix getRemote()** (remote.go:3-13)
  ```go
  func (c *cache) getRemote(bucket, key string) (any, error) {
      targets := c.rendevouz(bucket, key)
      if len(targets) == 0 {
          return nil, fmt.Errorf("no replica nodes available")
      }

      target := targets[0]  // Primary replica

      // Create cache GET request
      req := CacheRequest{
          Operation: CACHE_OP_GET,
          Bucket:    bucket,
          Key:       key,
      }

      payload, err := EncodeCacheRequest(req)
      if err != nil {
          return nil, fmt.Errorf("failed to encode request: %w", err)
      }

      // Send request and wait for response
      respData, err := c.app.Request(payload, target, 5*time.Second)
      if err != nil {
          return nil, fmt.Errorf("request failed: %w", err)
      }

      // Decode response
      resp, err := DecodeCacheResponse(respData)
      if err != nil {
          return nil, fmt.Errorf("failed to decode response: %w", err)
      }

      if !resp.Success {
          return nil, fmt.Errorf("cache error: %s", resp.Error)
      }

      // Deserialize value
      var value any
      err = gob.NewDecoder(bytes.NewReader(resp.Value)).Decode(&value)
      if err != nil {
          return nil, fmt.Errorf("failed to deserialize value: %w", err)
      }

      return value, nil
  }
  ```

- [ ] **Implement applicationReceiveTask()** (task.go:50-57)
  ```go
  func (c *cache) applicationReceiveTask(payload []byte, fromNode string) ([]byte, error) {
      // Decode request
      req, err := DecodeCacheRequest(payload)
      if err != nil {
          resp := CacheResponse{
              Success: false,
              Error:   fmt.Sprintf("failed to decode request: %v", err),
          }
          return EncodeCacheResponse(resp)
      }

      // Process based on operation
      var resp CacheResponse

      switch req.Operation {
      case CACHE_OP_GET:
          value, err := c.getLocal(req.Bucket, req.Key)
          if err != nil {
              resp = CacheResponse{
                  Success: false,
                  Error:   err.Error(),
              }
          } else {
              // Serialize value
              var buf bytes.Buffer
              err = gob.NewEncoder(&buf).Encode(value)
              if err != nil {
                  resp = CacheResponse{
                      Success: false,
                      Error:   fmt.Sprintf("failed to serialize value: %v", err),
                  }
              } else {
                  resp = CacheResponse{
                      Success: true,
                      Value:   buf.Bytes(),
                  }
              }
          }

      case CACHE_OP_PUT:
          // Deserialize value
          var value any
          err = gob.NewDecoder(bytes.NewReader(req.Value)).Decode(&value)
          if err != nil {
              resp = CacheResponse{
                  Success: false,
                  Error:   fmt.Sprintf("failed to deserialize value: %v", err),
              }
          } else {
              err = c.putLocal(req.Bucket, req.Key, value, 0)
              if err != nil {
                  resp = CacheResponse{
                      Success: false,
                      Error:   err.Error(),
                  }
              } else {
                  resp = CacheResponse{Success: true}
              }
          }

      case CACHE_OP_DELETE:
          err = c.deleteLocal(req.Bucket, req.Key)
          if err != nil {
              resp = CacheResponse{
                  Success: false,
                  Error:   err.Error(),
              }
          } else {
              resp = CacheResponse{Success: true}
          }

      case CACHE_OP_PUT_TTL:
          ttl := time.Duration(req.TTL)
          err = c.putTtlLocal(req.Bucket, req.Key, ttl)
          if err != nil {
              resp = CacheResponse{
                  Success: false,
                  Error:   err.Error(),
              }
          } else {
              resp = CacheResponse{Success: true}
          }

      default:
          resp = CacheResponse{
              Success: false,
              Error:   fmt.Sprintf("unknown operation: %d", req.Operation),
          }
      }

      return EncodeCacheResponse(resp)
  }
  ```

- [ ] **Register request handler in mycel Start()** (mycel.go)
  ```go
  func (c *cache) Start() error {
      // ... existing code ...

      c.app.SetRequestHandler(c.applicationReceiveTask)

      // ... rest of code ...
  }
  ```

- [ ] **Implement putRemote(), deleteRemote(), putTtlRemote()** following same pattern

- [ ] **Test distributed cache operations**

---

# PART 2: RACE CONDITIONS & CONCURRENCY BUGS

---

## BUG 1: Missing Lock in Stream Registry

**Severity**: MEDIUM (race condition)

**Location**: `internal/nodosum/registry.go:62-75`

**Issue**: `closeQuicStream()` accesses the stream map without acquiring a lock at line 63.

```go
func (n *Nodosum) closeQuicStream(nodeId, appId string) {
    key := fmt.Sprintf("%s:%s:data", nodeId, appId)
    stream, exists := n.quicApplicationStreams.streams[key]  // ❌ No lock!
    // ...
}
```

### Tasks

- [ ] **Add proper locking in closeQuicStream()**
  ```go
  func (n *Nodosum) closeQuicStream(nodeId, appId string) {
      key := fmt.Sprintf("%s:%s:data", nodeId, appId)

      n.quicApplicationStreams.RLock()
      stream, exists := n.quicApplicationStreams.streams[key]
      n.quicApplicationStreams.RUnlock()

      if !exists {
          return
      }

      // Close stream
      err := stream.Close()
      if err != nil {
          n.logger.Error("error closing quic stream", "error", err)
      }

      // Remove from registry
      n.quicApplicationStreams.Lock()
      delete(n.quicApplicationStreams.streams, key)
      n.quicApplicationStreams.Unlock()
  }
  ```

---

## BUG 2: Check-Then-Act Race in Stream Creation

**Severity**: LOW (could create duplicate streams)

**Location**: `internal/nodosum/registry.go:25-60`

**Issue**: Between checking if stream exists (lines 28-33) and creating it (lines 39-55), another goroutine could create the same stream.

**Current code:**
```go
func (n *Nodosum) getOrOpenQuicStream(nodeId, appId, streamName string) (quic.Stream, error) {
    key := fmt.Sprintf("%s:%s:%s", nodeId, appId, streamName)

    // Check if exists (READ lock)
    n.quicApplicationStreams.RLock()
    stream, exists := n.quicApplicationStreams.streams[key]
    n.quicApplicationStreams.RUnlock()

    if exists {
        return stream, nil
    }

    // Time gap here - another goroutine could create stream

    // Create new stream (WRITE lock)
    stream, err := conn.OpenStreamSync(n.ctx)
    // ...
}
```

### Tasks

- [ ] **Implement double-check locking pattern**
  ```go
  func (n *Nodosum) getOrOpenQuicStream(nodeId, appId, streamName string) (quic.Stream, error) {
      key := fmt.Sprintf("%s:%s:%s", nodeId, appId, streamName)

      // First check (read lock)
      n.quicApplicationStreams.RLock()
      stream, exists := n.quicApplicationStreams.streams[key]
      n.quicApplicationStreams.RUnlock()

      if exists {
          return stream, nil
      }

      // Get connection
      conn, err := n.getQuicConnection(nodeId)
      if err != nil {
          return nil, err
      }

      // Acquire write lock for creation
      n.quicApplicationStreams.Lock()
      defer n.quicApplicationStreams.Unlock()

      // Double-check - stream might have been created while waiting for lock
      stream, exists = n.quicApplicationStreams.streams[key]
      if exists {
          return stream, nil
      }

      // Now safe to create
      stream, err = conn.OpenStreamSync(n.ctx)
      if err != nil {
          return nil, err
      }

      // Send stream init frame
      streamInit := StreamInit{
          NodeID:        n.nodeID,
          ApplicationID: appId,
          StreamName:    streamName,
      }
      err = sendStreamInitFrame(stream, streamInit)
      if err != nil {
          stream.Close()
          return nil, err
      }

      // Register in map (already have write lock)
      n.quicApplicationStreams.streams[key] = stream

      return stream, nil
  }
  ```

---

## BUG 3: Stream Closure vs. Write Coordination

**Severity**: MEDIUM (could cause panics)

**Issue**: A stream could be closed by `closeQuicConnection()` while `Send()` is writing to it, causing "write on closed stream" error or panic.

**No explicit coordination between:**
- Stream writes in `Send()` (application.go:85-95)
- Stream closure in shutdown path
- Stream removal on errors

### Tasks

- [ ] **Add atomic closed flag to track stream state**
  ```go
  type streamWrapper struct {
      stream quic.Stream
      closed atomic.Bool
  }
  ```

- [ ] **Check closed flag before writes**
  ```go
  func (a *application) Send(payload []byte, targetNodes []string) error {
      for _, nodeID := range targetNodes {
          streamWrap, err := a.nodosum.getStreamWrapper(nodeID, a.uniqueIdentifier, "default")
          if err != nil {
              continue
          }

          if streamWrap.closed.Load() {
              a.logger.Debug("stream already closed, skipping", "node", nodeID)
              continue
          }

          err = sendDataFrame(streamWrap.stream, payload)
          if err != nil {
              a.logger.Error("failed to send", "node", nodeID, "error", err)
          }
      }
      return nil
  }
  ```

- [ ] **Set closed flag when closing streams**

---

## BUG 4: No Stream Cleanup on Read Errors

**Severity**: MEDIUM

**Issue**: When `streamReadLoop()` encounters an error and exits, the stream is not removed from the registry. Stale stream references accumulate.

### Tasks

- [ ] **Add cleanup in streamReadLoop() defer**
  ```go
  func (n *Nodosum) streamReadLoop(stream quic.Stream, nodeID, appID, streamName string) {
      defer func() {
          stream.Close()

          // Remove from registry
          key := fmt.Sprintf("%s:%s:%s", nodeID, appID, streamName)
          n.quicApplicationStreams.Lock()
          delete(n.quicApplicationStreams.streams, key)
          n.quicApplicationStreams.Unlock()

          n.logger.Debug("stream read loop exited", "key", key)
      }()

      // ... rest of read loop
  }
  ```

---

# PART 3: CLUSTERING ISSUES (From 3-Node Testing)

**STATUS: ✅ MOSTLY FIXED** - Most issues from 3-node testing have been resolved.

---

## ✅ Issue 1: Error Loop on Stream Accept After Connection Close [FIXED]

**Status**: ✅ **FIXED** in current codebase

**Original Symptom**: Error spam in logs when peer disconnects:
```
msg="error accepting quic stream: timeout: no recent network activity"
```

**Location**: `internal/nodosum/conn.go:51-82`

**Fix Applied**:
- ✅ `isQuicConnectionClosed()` helper implemented (conn.go:150-166)
- ✅ `handleQuicConn()` now exits loop on connection close (conn.go:67-71)
- ✅ Proper error differentiation between temporary and terminal errors

### Implementation (Already Complete)

The fix has been implemented in the current codebase. Here's what was done:

- ✅ **`isQuicConnectionClosed()` helper function** implemented in `conn.go:150-166`
  ```go
  func isQuicConnectionClosed(err error) bool {
      if err == nil {
          return false
      }
      // Checks for QUIC-specific errors, io.EOF, net.ErrClosed, etc.
  }
  ```

- ✅ **`handleQuicConn()` exits loop on terminal errors** (conn.go:51-82)
  - Uses `isQuicConnectionClosed()` to detect connection closure (line 67-71)
  - Returns from loop instead of continuing to spam logs
  - Log level is Debug for expected closures

**No further action needed** - this issue is resolved.

---

## ✅ Issue 2: Ready Signal Not Sent [MOSTLY FIXED]

**Status**: ✅ **MOSTLY FIXED** - Preventive measures implemented

**Original Symptom**: Sometimes memberlist initiates but `Ready()` timeout occurs - library doesn't signal ready.

**Location**: `internal/nodosum/nodosum.go:179-197`

**Fix Applied**:
- ✅ **sync.Once** prevents double Start() calls (nodosum.go:42, 190)
- ✅ **Defer** ensures readyChan always closes even on panic (nodosum.go:182-188)
- ✅ **Panic recovery** in NotifyJoin() prevents callback crashes (memberlist.go:19-24)

**Remaining investigation** (if issue still occurs):
- Add detailed logging in Start() to identify blocking points

### Implementation (Already Complete)

The following fixes have been implemented:

- ✅ **Panic recovery in NotifyJoin()** - memberlist.go:19-24
  - Prevents callback crashes from blocking ready signal

- ✅ **sync.Once in Start()** - nodosum.go:42, 190
  - Prevents double Start() calls that would panic on closing closed channel

- ✅ **Defer for readyChan** - nodosum.go:182-188
  - Ensures channel always closes even if panic occurs

**No further action needed** unless the ready signal timeout issue recurs in testing.

---

## ✅ Issue 3: IO Timeout and Nil Connection Handling [FIXED]

**Status**: ✅ **FIXED** in current codebase

**Original Symptom**: IO timeout errors when joining seed nodes, followed by potential panics from using nil connections.

**Location**: `internal/nodosum/memberlist.go:15-67`

**Fix Applied**:
- ✅ **Early return after Dial() error** (memberlist.go:42-45) - prevents nil connection usage
- ✅ **Dial timeout with 5-second context** (memberlist.go:39-41) - prevents indefinite hanging
- ✅ **Nil connection check** before CloseWithError (memberlist.go:51)
- ✅ **Proper lock management** - RLock released before dial operation

### Implementation (Already Complete)

The following fixes have been implemented in the current code:

- ✅ **Early returns on errors** - memberlist.go:42-45
  - Address resolution error returns immediately
  - Dial error returns immediately (no nil connection usage)

- ✅ **Dial timeout context** - memberlist.go:39-41
  ```go
  dialCtx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
  defer cancel()
  conn, err := d.quicTransport.Dial(dialCtx, addr, d.tlsConfig, d.quicConfig)
  ```

- ✅ **Nil check before CloseWithError** - memberlist.go:51

**No further action needed** - this issue is resolved.

---

## Testing Checklist

After implementing fixes:

- [ ] Test with 3-node cluster
- [ ] Test peer disconnect/reconnect (verify no error loops)
- [ ] Test network partition (verify graceful handling)
- [ ] Test node rejoin after crash
- [ ] Test simultaneous startup of all nodes
- [ ] Verify ready signal always arrives within timeout
- [ ] Check for connection leaks (file descriptors, goroutines)
- [ ] Monitor logs for unexpected errors

---

## Additional Improvements (Optional)

- [ ] Add metrics for QUIC connections (active count, total opened, errors)
- [ ] Add connection health monitoring (periodic ping)
- [ ] Implement exponential backoff for failed dials
- [ ] Add structured logging for better debugging
- [ ] Consider adding a `/debug/quic` HTTP endpoint showing connection status

---

## Notes

### Understanding the IO Timeout

The "IO timeout while joining initial seed nodes" is **expected behavior** when:
- Seed nodes are not reachable
- Seed nodes are slow to respond
- Network has packet loss
- First node in cluster (no seeds to join)

`memberlist.Join()` has built-in timeouts and will return an error, but `Start()` continues normally. This is correct behavior - the issue is that the error handling in `NotifyJoin()` doesn't properly handle the nil connection case.

### QUIC Connection Lifecycle

```
1. Memberlist gossip: "Node A joined"
2. NotifyJoin(A) callback fires
3. Dial QUIC connection to A
4. Start handleQuicConn() goroutine
5. Accept streams in loop
6. Node A disconnects
7. AcceptStream() returns error
8. [BUG] Loop continues forever
```

The fix ensures step 8 exits the loop instead of continuing.

---

# IMPLEMENTATION ROADMAP

## Phase 1: Fix Stream Communication (CRITICAL - DO FIRST)

**Goal**: Make basic one-way messaging work

**Estimated effort**: 1 day

**Tasks**:
1. Implement `streamReadLoop()` in conn.go
2. Implement `routeToApplication()` in conn.go
3. Update `handleStream()` to launch read loop
4. Fix `applicationReceiveTask` struct/interface for routing messages
5. Test one-way Send() works end-to-end

**Success criteria**:
- Application A can send message to Application B on remote node
- Application B receives the message via receiveFunc callback
- No errors in logs during normal operation

**Files to modify**:
- `internal/nodosum/conn.go` (primary changes)
- `internal/nodosum/application.go` (minor routing changes)

---

## Phase 2: Add Request-Response Pattern (HIGH PRIORITY)

**Goal**: Enable cache and other applications to request data and wait for response

**Estimated effort**: 1-2 days

**Tasks**:
1. Add `FRAME_TYPE_REQUEST` and `FRAME_TYPE_RESPONSE` to quic.go (lines 9-12)
2. Implement `RequestFrame` and `ResponseFrame` structs with encoding/decoding in quic.go
3. Add encoding/decoding functions for REQUEST/RESPONSE frames in quic.go
4. Add `pendingRequests` map to application struct (application.go)
5. Implement `Request()` method with correlation IDs and timeout (5s default) (application.go)
6. Implement `SetRequestHandler()` method (application.go)
7. Implement `handleRequest()` in conn.go
8. Implement `handleResponse()` in conn.go
9. Implement `routeToApplication()` in conn.go (needed by streamReadLoop)
10. Update `streamReadLoop()` to route REQUEST/RESPONSE frames
11. Test concurrent requests work correctly
12. Test timeout handling

**Success criteria**:
- Application can call `Request()` and get response back
- Timeouts work correctly (return error after 5 seconds)
- Multiple concurrent requests work without conflicts
- Request handlers can return errors properly

**Files to modify**:
- `internal/nodosum/quic.go` (new frame types, encoding/decoding functions)
- `internal/nodosum/application.go` (Request method, SetRequestHandler, pendingRequests)
- `internal/nodosum/conn.go` (handleRequest, handleResponse, routeToApplication)

---

## Phase 3: Implement Named Streams (MEDIUM PRIORITY)

**Goal**: Allow applications to create multiple streams per node for different purposes

**Estimated effort**: 4-6 hours

**Tasks**:
1. Change default stream name from "data" to "default"
2. Update all stream key formats to use streamName parameter
3. Implement `OpenNamedStream()` method
4. Implement `SendOnStream()` method
5. Update `Send()` to use "default" stream
6. Test multiple streams per app-node pair

**Success criteria**:
- Default Send() uses "default" stream
- Applications can create named streams
- Multiple streams to same node work independently

**Files to modify**:
- `internal/nodosum/registry.go` (stream key format)
- `internal/nodosum/application.go` (new methods)

---

## Phase 4: Fix Cache Layer (HIGH PRIORITY)

**Goal**: Make distributed cache operations work

**Estimated effort**: 1 day

**Tasks**:
1. Create `internal/mycel/protocol.go` with cache request/response types
2. Implement encoding/decoding using encoding/gob
3. Rewrite `getRemote()` to use Request() method
4. Implement `applicationReceiveTask()` to handle cache requests
5. Register request handler in mycel `Start()`
6. Implement `putRemote()`, `deleteRemote()`, `putTtlRemote()`
7. Add proper error handling and logging
8. Test all distributed cache operations

**Success criteria**:
- `Get()` can retrieve values from remote nodes
- `Put()` replicates to remote nodes
- `Delete()` works across nodes
- Errors are properly propagated
- Timeouts work correctly

**Files to modify**:
- `internal/mycel/protocol.go` (NEW FILE)
- `internal/mycel/remote.go` (complete rewrite)
- `internal/mycel/task.go` (implement handler)
- `internal/mycel/mycel.go` (register handler)
- `internal/mycel/cache.go` (minor updates for remote ops)

---

## Phase 5: Fix Race Conditions (MEDIUM PRIORITY)

**Goal**: Eliminate all race conditions and concurrency bugs

**Estimated effort**: 1 day

**Tasks**:
1. Fix missing lock in `closeQuicStream()` (registry.go:63)
2. Implement double-check locking in `getOrOpenQuicStream()`
3. Add stream wrapper with atomic closed flag
4. Update Send() to check closed flag before writes
5. Add stream cleanup in `streamReadLoop()` defer
6. Run race detector: `go test -race -v ./...`
7. Fix any additional races found

**Success criteria**:
- `go test -race` passes with no race warnings
- No stale streams accumulate in registry
- No "write on closed stream" errors
- Graceful shutdown works correctly

**Files to modify**:
- `internal/nodosum/registry.go` (locking fixes)
- `internal/nodosum/conn.go` (stream cleanup)
- `internal/nodosum/application.go` (closed flag checks)

---

## ✅ Phase 6: Fix Clustering Issues [MOSTLY COMPLETE]

**Status**: ✅ **MOSTLY COMPLETE** - Core fixes implemented

**Goal**: Fix operational issues from 3-node testing

**Original estimated effort**: 4-6 hours

**Completed Tasks**:
1. ✅ Add `isQuicConnectionClosed()` helper (conn.go:150-166)
2. ✅ Fix `handleQuicConn()` to exit loop on connection close (conn.go:67-71)
3. ✅ Add early returns after Dial() errors in `NotifyJoin()` (memberlist.go:42-45)
4. ✅ Fix RUnlock timing in memberlist.go
5. ✅ Add nil checks before CloseWithError (memberlist.go:51)
6. ✅ Add panic recovery in NotifyJoin (memberlist.go:19-24)
7. ✅ Add sync.Once to prevent double Start() (nodosum.go:42, 190)
8. ✅ Add defer for readyChan (nodosum.go:182-188)

**Remaining (if needed)**:
- Add detailed logging in `Start()` (only if ready signal issue recurs)
- Test with 3-node cluster to verify fixes

**Success criteria** (expected to be met):
- ✅ No error loops when nodes disconnect
- ✅ Graceful handling of dial failures
- ✅ Clean logs during normal operation
- ⚠️ Ready signal should always arrive (needs verification in testing)

**Files modified**:
- ✅ `internal/nodosum/conn.go` (connection close handling)
- ✅ `internal/nodosum/memberlist.go` (error handling)
- ✅ `internal/nodosum/nodosum.go` (ready signal fixes)

---

## Phase 7: Comprehensive Testing (FINAL)

**Goal**: Ensure everything works reliably

**Estimated effort**: 1-2 days

**Tests to write**:
1. Unit tests for protocol encoding/decoding
2. Unit tests for correlation ID system
3. Integration tests for one-way messaging
4. Integration tests for request-response
5. Integration tests for cache operations
6. Cluster tests with 3+ nodes
7. Chaos tests (random disconnect/reconnect)
8. Load tests (concurrent operations)
9. Error path tests (timeouts, failures)

**Manual testing**:
1. 3-node cluster startup
2. Node disconnect/reconnect
3. Network partition simulation
4. Graceful shutdown
5. Cache operations under load
6. Concurrent cache requests

**Success criteria**:
- All tests pass
- Race detector clean
- No goroutine leaks
- No memory leaks
- Logs are clean
- Performance acceptable

---

## Summary: Estimated Total Effort

| Phase | Priority | Status | Effort | Dependencies |
|-------|----------|--------|--------|--------------|
| Phase 1: Stream Communication | CRITICAL | ❌ TODO | 1 day | None |
| Phase 2: Request-Response | HIGH | ❌ TODO | 1-2 days | Phase 1 |
| Phase 3: Named Streams | MEDIUM | ❌ TODO | 4-6 hours | Phase 1 |
| Phase 4: Cache Layer | HIGH | ❌ TODO | 1 day | Phase 1, 2 |
| Phase 5: Race Conditions | MEDIUM | ❌ TODO | 1 day | Phase 1 |
| Phase 6: Clustering Issues | MEDIUM | ✅ DONE | ~0 days | None |
| Phase 7: Testing | FINAL | ❌ TODO | 1-2 days | All phases |
| **TOTAL REMAINING** | | | **~5-7 days** | |

## Recommended Order of Implementation

**Week 1 (Make it work)** - ~5 days:
1. Day 1: Phase 1 (Stream communication) - **CRITICAL**
2. Day 2-3: Phase 2 (Request-response) - **HIGH PRIORITY**
3. Day 4: Phase 4 (Cache layer) - **HIGH PRIORITY**
4. Day 5: Initial testing

**Week 2 (Make it robust)** - ~2-3 days:
1. Day 1: Phase 5 (Race conditions)
2. Day 2: Phase 3 (Named streams - optional enhancement)
3. Day 3-4: Phase 7 (Comprehensive testing)

**Note**: Phase 6 (Clustering issues) is ✅ **ALREADY COMPLETE** - no work needed unless issues surface in testing.

## Quick Win Path (Minimum Viable)

If you want the library usable ASAP, do this subset:

1. **Phase 1** - Stream communication (MUST HAVE)
2. **Phase 2** - Request-response (MUST HAVE)
3. **Partial Phase 4** - Just getRemote() for cache (MUST HAVE)
4. **Basic testing** - One integration test

**Estimated**: 3-4 days to have working library

Then iterate on:
- Named streams (optional)
- Complete cache layer (put/delete remote)
- Race condition fixes (important for production)
- Clustering issue fixes (important for production)
- Comprehensive testing (important for production)

---

## Key Decision Points

### Correlation ID Storage

**Question**: Should pendingRequests map be in Application or Nodosum?

**Recommendation**: Keep in Application struct
- Each application manages its own pending requests
- Simpler scoping and cleanup
- Apps can have different timeout strategies

**Trade-off**: `handleResponse()` must iterate through all applications to find correlation ID. This is acceptable for moderate request volume.

### Encoding Format

**Question**: Use encoding/gob, protobuf, or msgpack for cache protocol?

**Recommendation**: Start with encoding/gob
- Built into Go standard library
- Simple to use
- Good performance for Go-to-Go communication

**Future**: Can switch to protobuf if cross-language support needed

### Stream Wrapper vs. Closed Flag

**Question**: Wrap stream in struct or add map of closed flags?

**Recommendation**: Wrap stream in struct
- Cleaner API
- Easier to extend with metrics later
- Type safety

### Error Handling Philosophy

**Question**: Should cache return partial results if some replicas fail?

**Recommendation**: Start with fail-fast
- Return error if primary replica fails
- Simpler to reason about
- Can add fallback to secondary replicas later

---

## After Migration Complete

Once all phases done, consider:

1. **Performance optimization**
   - Benchmark cache operations
   - Profile memory usage
   - Optimize hot paths

2. **Observability**
   - Add Prometheus metrics
   - Add request tracing
   - Add /debug endpoints

3. **Reliability**
   - Add circuit breakers
   - Add retry logic with backoff
   - Add request hedging

4. **Features**
   - Cache replication factor > 1
   - Consistent hashing for better distribution
   - Cache invalidation broadcasts
   - TTL synchronization across nodes

5. **Documentation**
   - Update CLAUDE.md
   - Add architecture diagrams
   - Write API documentation
   - Create example applications

---

## 📋 REVISION HISTORY

**Last updated**: December 2024 (after big cleanup of old TCP/UDP code)

### Changes from Original Analysis

**IMPORTANT FILE NAMING CORRECTION**:
- Protocol implementation is in `internal/nodosum/quic.go`, NOT `protocol.go`
- All references to "protocol.go" for nodosum have been corrected to "quic.go"
- Cache layer still needs a NEW file `internal/mycel/protocol.go` to be created

**✅ COMPLETED SINCE LAST UPDATE** (Phase 6 - Clustering Issues):
1. Connection close detection - `isQuicConnectionClosed()` helper implemented (conn.go:193-209)
2. Error loop fix - `handleQuicConn()` now exits gracefully on connection close (conn.go:67-71)
3. Nil connection handling - Early returns prevent nil pointer panics (memberlist.go:42-45)
4. Dial timeout - 5-second context prevents hanging (memberlist.go:39-41)
5. Panic recovery - NotifyJoin() has panic recovery to prevent callback crashes (memberlist.go:19-24)
6. sync.Once - Prevents double Start() calls (nodosum.go:42, 190)
7. Defer for readyChan - Ensures ready signal always sent even on panic (nodosum.go:182-188)

**❌ CRITICAL WORK REMAINING** (Phases 1, 2, 4):

**Phase 1 - Stream Communication** (BLOCKS EVERYTHING):
1. Fix `streamReadLoop()` syntax error at line 158 (conn.go)
2. Remove references to non-existent REQUEST/RESPONSE frames (or implement them in Phase 2)
3. Implement `routeToApplication()` function (conn.go)
4. Update `handleStream()` to launch read loop with `go n.streamReadLoop(...)`

**Phase 2 - Request-Response Pattern** (NEEDED FOR CACHE):
1. Add FRAME_TYPE_REQUEST (0x03) and FRAME_TYPE_RESPONSE (0x04) to quic.go
2. Add RequestFrame/ResponseFrame structs and encoding/decoding functions to quic.go
3. Add Request() method to Application interface (application.go)
4. Add SetRequestHandler() method to Application interface (application.go)
5. Implement handleRequest() and handleResponse() in conn.go

**Phase 4 - Cache Layer** (HIGH PRIORITY):
1. Create NEW FILE: internal/mycel/protocol.go with cache request/response types
2. Fix remote.go:3-13 (currently sends nil, returns wrong data)
3. Fix task.go:50-57 (uses non-existent binary.Decode)
4. Add PUT, DELETE, PUT_TTL request types to dto.go

**⚠️ MEDIUM PRIORITY REMAINING** (Phases 3, 5):
1. Named stream support - Not implemented (currently only "data" stream)
2. Race conditions - 2 known issues in registry.go (lines 63, 28-39)

### Migration Progress

- **Original estimate**: ~60% complete
- **Current state**: ~70% complete (more accurate after code review)
- **Work completed**: Phase 6 (Clustering Issues) ✅
- **Estimated remaining**: ~5-7 days

### Next Steps

**START HERE**: Focus on **Phase 1** (Stream Communication) first:
1. Fix the syntax error in streamReadLoop() line 158
2. Simplify it to only handle DATA frames for now
3. Implement routeToApplication() to deliver messages
4. Update handleStream() to launch the read loop
5. Test that one-way messaging works

This will unblock basic functionality and allow applications to receive messages.
