# QUIC Migration Guide - Updated for Actual Codebase

This guide documents the QUIC migration status and remaining work based on the actual current state of the code.

## Current Status

### ✅ Part 1: Critical Bug Fixes - **COMPLETED**

All critical bugs have been fixed:
- ✅ Nil pointer panic in accept loop fixed (`conn.go:35-44`)
- ✅ Defensive nil check in `handleQuicConn()` (`conn.go:52-55`)
- ✅ Missing RUnlock fixed in `memberlist.go` NotifyJoin (`memberlist.go:26-32`)
- ✅ Nil pointer panic after Dial error fixed (`memberlist.go:42-44`)
- ✅ Panic recovery added to NotifyJoin (`memberlist.go:19-24`)
- ✅ Stream cleanup in `closeQuicConnection()` implemented (`conn.go:84-120`)

**No more shutdown panics!** The system now handles connection errors gracefully.

---

## Table of Contents

1. [~~Critical Bug Fixes~~](#part-1-critical-bug-fixes---completed) ✅ **DONE**
2. [Fix Stream Read Loop](#part-2-fix-stream-read-loop) ⚠️ **CRITICAL - IN PROGRESS**
3. [Implement Application Message Routing](#part-3-implement-application-message-routing) ⚠️ **CRITICAL TODO**
4. [Request-Response Patterns](#part-4-request-response-patterns) ⚠️ **TODO** (HIGH PRIORITY)
5. [Testing and Validation](#part-5-testing-and-validation)

---

## Important File Structure Notes

**The protocol implementation is in `quic.go`, NOT `protocol.go`**

### Actual Files in `internal/nodosum/`:
- `quic.go` - Protocol constants, frame encoding/decoding
- `conn.go` - Connection and stream handling
- `application.go` - Application interface and Send() implementation
- `registry.go` - Stream registry and `getOrOpenQuicStream()`
- `memberlist.go` - Memberlist event delegate
- `nodosum.go` - Core orchestration and lifecycle
- `cert.go` - TLS certificate generation
- `config.go` - Configuration structs

### Files that DO NOT exist:
- ❌ `protocol.go` - This does not exist, use `quic.go` instead
- ❌ `mux.go` - Old multiplexer code was removed

---

## Part 2: Fix Stream Read Loop ⚠️ CRITICAL

### Current Problem: Broken streamReadLoop Implementation

**File:** `internal/nodosum/conn.go:153-181`

The `streamReadLoop()` function exists but is broken:

```go
// CURRENT BROKEN CODE (conn.go:153-181)
func (n *Nodosum) streamReadLoop(stream *quic.Stream, nodeID, appID string) {
	defer stream.Close()

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
			// Read next frame using existing readFrame() function from quic.go
			frameType, payload, err := readFrame(stream)  // ✅ This works
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
				n.routeToApplication(appID, payload, nodeID)  // ❌ DOES NOT EXIST
			default:
				n.logger.Warn("unknown frame type", "type", frameType)
			}
		}
	}
}
```

**Problem:** The function calls `n.routeToApplication()` which doesn't exist anywhere in the codebase.

### Current handleStream Implementation

**File:** `internal/nodosum/conn.go:122-151`

```go
// CURRENT CODE (conn.go:122-151) - HAS ISSUES
func (n *Nodosum) handleStream(stream *quic.Stream) {

	frameType, frameLen, err := readFrame(stream)  // ❌ Line 124: variables declared but not used

	if stream == nil {  // ❌ Should be checked first
		return
	}

	frameType := make([]byte, 1)  // ❌ Redeclared variable

	_, err := io.ReadFull(stream, frameType)  // ❌ Redeclared err
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

	go n.streamReadLoop(stream, streamInit.NodeID, streamInit.ApplicationID)  // ❌ streamInit undefined

	n.logger.Debug(fmt.Sprintf("Registered stream, %s, %s, %s", nodeId, appId, streamName))
}
```

**Problems:**
1. Multiple variable redeclarations
2. References undefined `streamInit` variable
3. Attempts to read frame twice (once with `readFrame`, once manually)
4. No error handling for decoding failures

---

## Part 3: Implement Application Message Routing ⚠️ CRITICAL

### Missing Function: routeToApplication

This function is called by `streamReadLoop()` but doesn't exist. It needs to deliver received messages to the appropriate application's receive worker.

**Add to:** `internal/nodosum/conn.go` or `internal/nodosum/application.go`

```go
func (n *Nodosum) routeToApplication(appID string, payload []byte, fromNode string) {
	// Look up the application
	val, ok := n.applications.Load(appID)
	if !ok {
		n.logger.Warn("received message for unknown application", "appID", appID, "fromNode", fromNode)
		return
	}

	app := val.(*application)

	// Send to application's receive worker
	select {
	case app.receiveWorker.InputChan <- payload:
		// Successfully delivered
	case <-n.ctx.Done():
		// Shutting down
		return
	default:
		n.logger.Warn("application receive channel full, dropping message",
			"appID", appID, "fromNode", fromNode)
	}
}
```

### Update Application Receive Task

**File:** `internal/nodosum/application.go:121-123`

The current implementation just logs a warning. It should invoke the registered callback:

```go
func (n *Nodosum) applicationReceiveTask(w *worker.Worker, msg any) {
	// This is the default task, but when SetReceiveFunc is called,
	// it replaces w.TaskFunc with the user's callback
	w.Logger.Warn("application receive callback is not set")
}
```

This is actually correct - `SetReceiveFunc()` at line 107-115 properly replaces the TaskFunc.

---

## Part 4: Request-Response Patterns ⚠️ TODO (HIGH PRIORITY)

### Problem Statement

The current Application interface only supports **one-way message passing**:

```go
// CURRENT (application.go:9-16)
type Application interface {
	Send(payload []byte, ids []string) error        // Fire and forget
	SetReceiveFunc(func(payload []byte) error)      // Just receives
	Nodes() []string
}
```

**Critical issue**: No way to correlate responses with requests!

This is why Mycel's cache operations can't work across nodes - there's no way to send a GET request and wait for the response.

### Recommended Solution: Add Correlation IDs

**Step 1: Add new frame types to quic.go**

**File:** `internal/nodosum/quic.go` (add after line 12)

```go
const (
	FRAME_TYPE_STREAM_INIT = 0x01  // Existing
	FRAME_TYPE_DATA        = 0x02  // Existing
	FRAME_TYPE_REQUEST     = 0x03  // NEW
	FRAME_TYPE_RESPONSE    = 0x04  // NEW
)
```

**Step 2: Add request/response frame encoding**

**File:** `internal/nodosum/quic.go` (add at end)

```go
// encodeRequestFrame creates a request frame with correlation ID
// Frame format: [type:1][corrID_len:2][corrID:N][payload_len:4][payload:N]
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

// decodeRequestFrame reads a request frame and returns correlation ID and payload
func decodeRequestFrame(stream io.Reader) (correlationID string, payload []byte, err error) {
	// Read correlation ID length
	lenBuf := make([]byte, 2)
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		return "", nil, err
	}
	corrIDLen := binary.BigEndian.Uint16(lenBuf)

	// Read correlation ID
	corrIDBuf := make([]byte, corrIDLen)
	if _, err := io.ReadFull(stream, corrIDBuf); err != nil {
		return "", nil, err
	}
	correlationID = string(corrIDBuf)

	// Read payload length
	payloadLenBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, payloadLenBuf); err != nil {
		return "", nil, err
	}
	payloadLen := binary.BigEndian.Uint32(payloadLenBuf)

	// Read payload
	payload = make([]byte, payloadLen)
	if _, err := io.ReadFull(stream, payload); err != nil {
		return "", nil, err
	}

	return correlationID, payload, nil
}

// encodeResponseFrame creates a response frame
// Frame format: [type:1][corrID_len:2][corrID:N][success:1][payload_len:4][payload:N]
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

// decodeResponseFrame reads a response frame
func decodeResponseFrame(stream io.Reader) (correlationID string, success bool, payload []byte, err error) {
	// Read correlation ID length
	lenBuf := make([]byte, 2)
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		return "", false, nil, err
	}
	corrIDLen := binary.BigEndian.Uint16(lenBuf)

	// Read correlation ID
	corrIDBuf := make([]byte, corrIDLen)
	if _, err := io.ReadFull(stream, corrIDBuf); err != nil {
		return "", false, nil, err
	}
	correlationID = string(corrIDBuf)

	// Read success flag
	successBuf := make([]byte, 1)
	if _, err := io.ReadFull(stream, successBuf); err != nil {
		return "", false, nil, err
	}
	success = successBuf[0] == 1

	// Read payload length
	payloadLenBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, payloadLenBuf); err != nil {
		return "", false, nil, err
	}
	payloadLen := binary.BigEndian.Uint32(payloadLenBuf)

	// Read payload
	payload = make([]byte, payloadLen)
	if _, err := io.ReadFull(stream, payload); err != nil {
		return "", false, nil, err
	}

	return correlationID, success, payload, nil
}
```

**Step 3: Enhance Application Interface**

**File:** `internal/nodosum/application.go`

Add to the Application interface:

```go
type Application interface {
	// Existing methods
	Send(payload []byte, ids []string) error
	SetReceiveFunc(func(payload []byte) error)
	Nodes() []string

	// New request-response methods
	Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error)
	SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error))
}

var (
	ErrRequestTimeout = errors.New("request timeout")
	ErrNoHandler      = errors.New("no request handler registered")
)
```

Update the application struct:

```go
type application struct {
	id              string
	nodosum         *Nodosum
	receiveFunc     func(payload []byte) error
	requestHandler  func(payload []byte, fromNode string) ([]byte, error)  // NEW
	nodes           []string
	receiveWorker   *worker.Worker
	pendingRequests *sync.Map  // NEW - map[correlationID]chan response
}
```

**Step 4: Implement Request() Method**

```go
func (a *application) Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error) {
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	// Generate correlation ID using github.com/google/uuid (already in dependencies)
	correlationID := uuid.New().String()

	// Create response channel
	responseChan := make(chan []byte, 1)
	a.pendingRequests.Store(correlationID, responseChan)
	defer a.pendingRequests.Delete(correlationID)

	// Get stream
	stream, err := a.nodosum.getOrOpenQuicStream(targetNode, a.id, "data")
	if err != nil {
		return nil, fmt.Errorf("failed to get stream: %w", err)
	}

	// Send request frame
	frame := encodeRequestFrame(correlationID, payload)
	_, err = (*stream).Write(frame)
	if err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	// Wait for response
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case response := <-responseChan:
		return response, nil
	case <-timer.C:
		return nil, ErrRequestTimeout
	case <-a.receiveWorker.Ctx.Done():
		return nil, errors.New("application shutting down")
	}
}
```

**Step 5: Implement SetRequestHandler()**

```go
func (a *application) SetRequestHandler(handler func(payload []byte, fromNode string) ([]byte, error)) {
	a.requestHandler = handler
}
```

**Step 6: Update streamReadLoop to handle REQUEST/RESPONSE frames**

**File:** `internal/nodosum/conn.go` - Update the streamReadLoop function:

```go
func (n *Nodosum) streamReadLoop(stream *quic.Stream, nodeID, appID string) {
	defer stream.Close()

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
			frameType, payload, err := readFrame(stream)
			if err != nil {
				if isQuicConnectionClosed(err) {
					n.logger.Debug("stream closed", "nodeID", nodeID, "appID", appID)
					return
				}
				n.logger.Error("error reading frame", "error", err)
				return
			}

			switch frameType {
			case FRAME_TYPE_DATA:
				n.routeToApplication(appID, payload, nodeID)

			case FRAME_TYPE_REQUEST:
				n.handleRequest(stream, appID, nodeID)

			case FRAME_TYPE_RESPONSE:
				n.handleResponse(appID, nodeID, stream)

			default:
				n.logger.Warn("unknown frame type", "type", frameType)
			}
		}
	}
}
```

**Step 7: Implement handleRequest and handleResponse**

Add these functions to `internal/nodosum/conn.go`:

```go
func (n *Nodosum) handleRequest(stream *quic.Stream, appID, fromNode string) {
	// Decode request frame (frame type already read by readFrame)
	correlationID, payload, err := decodeRequestFrame(stream)
	if err != nil {
		n.logger.Error("failed to decode request", "error", err)
		return
	}

	// Get application
	val, ok := n.applications.Load(appID)
	if !ok {
		// Send error response
		errFrame := encodeResponseFrame(correlationID, false, []byte("application not found"))
		(*stream).Write(errFrame)
		return
	}

	app := val.(*application)

	// Check if handler is registered
	if app.requestHandler == nil {
		errFrame := encodeResponseFrame(correlationID, false, []byte("no request handler"))
		(*stream).Write(errFrame)
		return
	}

	// Invoke handler
	response, err := app.requestHandler(payload, fromNode)
	if err != nil {
		errFrame := encodeResponseFrame(correlationID, false, []byte(err.Error()))
		(*stream).Write(errFrame)
		return
	}

	// Send success response
	respFrame := encodeResponseFrame(correlationID, true, response)
	(*stream).Write(respFrame)
}

func (n *Nodosum) handleResponse(appID, fromNode string, stream *quic.Stream) {
	// Decode response frame
	correlationID, success, payload, err := decodeResponseFrame(stream)
	if err != nil {
		n.logger.Error("failed to decode response", "error", err)
		return
	}

	// Get application
	val, ok := n.applications.Load(appID)
	if !ok {
		n.logger.Warn("received response for unknown application", "appID", appID)
		return
	}

	app := val.(*application)

	// Find pending request
	chanVal, ok := app.pendingRequests.Load(correlationID)
	if !ok {
		n.logger.Warn("received response for unknown correlation ID",
			"correlationID", correlationID, "appID", appID)
		return
	}

	responseChan := chanVal.(chan []byte)

	// Deliver response (non-blocking)
	if success {
		select {
		case responseChan <- payload:
		default:
			n.logger.Warn("response channel full", "correlationID", correlationID)
		}
	} else {
		// For errors, send empty payload (the Request() method will timeout or get nil)
		select {
		case responseChan <- nil:
		default:
		}
	}
}
```

**Step 8: Update RegisterApplication to initialize pendingRequests**

**File:** `internal/nodosum/application.go:32-57`

```go
func (n *Nodosum) RegisterApplication(uniqueIdentifier string) Application {
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
		id:              uniqueIdentifier,
		nodosum:         n,
		receiveWorker:   receiveWorker,
		nodes:           nodes,
		pendingRequests: &sync.Map{},  // ADD THIS LINE
	}

	n.applications.Store(uniqueIdentifier, app)
	return app
}
```

---

## Part 5: Testing and Validation

### Test 1: Basic Point-to-Point Messaging

```go
func TestBasicMessaging(t *testing.T) {
	// Setup two nodes
	node1, _ := setupTestNode("node-1")
	node2, _ := setupTestNode("node-2")
	defer node1.Shutdown()
	defer node2.Shutdown()

	// Register applications
	app1 := node1.RegisterApplication("test-app")
	app2 := node2.RegisterApplication("test-app")

	received := make(chan string, 1)
	app2.SetReceiveFunc(func(payload []byte) error {
		received <- string(payload)
		return nil
	})

	// Send message
	err := app1.Send([]byte("hello"), []string{node2.Id()})
	require.NoError(t, err)

	// Verify received
	select {
	case msg := <-received:
		assert.Equal(t, "hello", msg)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}
```

### Test 2: Request-Response

```go
func TestRequestResponse(t *testing.T) {
	node1, _ := setupTestNode("node-1")
	node2, _ := setupTestNode("node-2")
	defer node1.Shutdown()
	defer node2.Shutdown()

	app1 := node1.RegisterApplication("test-app")
	app2 := node2.RegisterApplication("test-app")

	// Set request handler
	app2.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
		return append([]byte("echo: "), payload...), nil
	})

	// Send request
	response, err := app1.Request([]byte("hello"), node2.Id(), 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "echo: hello", string(response))
}
```

### Test 3: Request Timeout

```go
func TestRequestTimeout(t *testing.T) {
	node1, _ := setupTestNode("node-1")
	node2, _ := setupTestNode("node-2")
	defer node1.Shutdown()
	defer node2.Shutdown()

	app1 := node1.RegisterApplication("test-app")
	app2 := node2.RegisterApplication("test-app")

	// Handler that sleeps
	app2.SetRequestHandler(func(payload []byte, fromNode string) ([]byte, error) {
		time.Sleep(10 * time.Second)
		return payload, nil
	})

	// Request with short timeout
	start := time.Now()
	_, err := app1.Request([]byte("test"), node2.Id(), 1*time.Second)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, ErrRequestTimeout)
	assert.Less(t, elapsed, 2*time.Second)
}
```

---

## Migration Checklist

### Phase 1: Fix Stream Reading ⚠️ CRITICAL
- [ ] Fix `handleStream()` implementation (conn.go:122-151)
  - Remove duplicate variable declarations
  - Properly read STREAM_INIT frame
  - Launch streamReadLoop as goroutine
- [ ] Fix `streamReadLoop()` (conn.go:153-181)
  - Already mostly correct, just needs routeToApplication
- [ ] Implement `routeToApplication()` function
- [ ] Test basic one-way messaging works

### Phase 2: Request-Response Implementation ⚠️ HIGH PRIORITY
- [ ] Add FRAME_TYPE_REQUEST and FRAME_TYPE_RESPONSE to quic.go
- [ ] Implement encodeRequestFrame() and decodeRequestFrame()
- [ ] Implement encodeResponseFrame() and decodeResponseFrame()
- [ ] Add Request() method to Application interface
- [ ] Add SetRequestHandler() method to Application interface
- [ ] Add pendingRequests field to application struct
- [ ] Implement Request() method in application.go
- [ ] Implement SetRequestHandler() in application.go
- [ ] Update streamReadLoop() to handle REQUEST/RESPONSE frames
- [ ] Implement handleRequest() function
- [ ] Implement handleResponse() function
- [ ] Update RegisterApplication() to initialize pendingRequests
- [ ] Test request-response works end-to-end
- [ ] Test timeout handling
- [ ] Test concurrent requests (100+)

### Phase 3: Cache Layer Integration
- [ ] Fix Mycel remote.go to use Request() instead of Send()
- [ ] Implement cache request handler in Mycel
- [ ] Test cache Get() across nodes
- [ ] Test cache Put() across nodes

### Phase 4: Testing
- [ ] Unit tests for frame encoding/decoding
- [ ] Integration tests for messaging
- [ ] Integration tests for request-response
- [ ] Load tests with concurrent operations
- [ ] Test graceful shutdown
- [ ] Test error recovery

---

## Key Implementation Notes

1. **Always use quic.Stream pointers**: The registry stores `*quic.Stream`, not `quic.Stream`
2. **Function name is `getOrOpenQuicStream`**, not `getOrCreateQuicStream`
3. **Protocol constants are in `quic.go`**, not `protocol.go`
4. **The `readFrame()` function already exists** in quic.go:163-197
5. **UUID generation**: Use `github.com/google/uuid` (already in dependencies)
6. **Existing helper**: `getConnectedNodes()` in quic.go:199-209 returns list of connected node IDs
7. **Existing helper**: `encodeDataFrame()` in quic.go:127-138 for DATA frames
8. **Existing helper**: `isQuicConnectionClosed()` in conn.go:185-201 for error checking

---

## Common Pitfalls to Avoid

1. Don't create `protocol.go` - use `quic.go` instead
2. Don't reference `mux.go` - it doesn't exist anymore
3. Don't use `getOrCreateQuicStream` - the actual function is `getOrOpenQuicStream`
4. Don't forget to launch goroutines for read loops
5. Don't forget to close streams in defer statements
6. Don't block on channel sends - use select with timeout or default case
7. Don't forget to clean up pendingRequests on timeout or shutdown

---

## Summary

The migration is approximately **70% complete**:

✅ **Working:**
- QUIC connection establishment and management
- Stream creation and registration
- Basic frame protocol (STREAM_INIT, DATA)
- Send() implementation writes to streams
- Connection cleanup and graceful shutdown

⚠️ **Critical Issues:**
- `handleStream()` has compilation errors
- `streamReadLoop()` calls non-existent `routeToApplication()`
- No request-response correlation system
- Applications never receive messages

🎯 **Next Steps:**
1. Fix handleStream() and implement routeToApplication() - **blocks all functionality**
2. Implement request-response with correlation IDs - **required for cache**
3. Test thoroughly across multiple nodes
4. Document and deploy

Good luck completing the migration! 🚀
