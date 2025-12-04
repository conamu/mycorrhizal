# QUIC Clustering Issues - TODO List

Issues discovered during 3-node cluster testing.

---

## Issue 1: Error Loop on Stream Accept After Connection Close

**Symptom**: Error spam in logs when peer disconnects:
```
msg="error accepting quic stream: timeout: no recent network activity"
```

**Location**: `internal/nodosum/conn.go:47-61`

**Root Cause**: `handleQuicConn()` loops forever even after connection is closed, continuously trying to accept streams and logging errors.

### Tasks

- [ ] **Add `isConnectionClosed()` helper function** in `conn.go`
  ```go
  func isConnectionClosed(err error) bool {
      if err == nil {
          return false
      }

      // QUIC-specific errors
      var appErr *quic.ApplicationError
      var idleErr *quic.IdleTimeoutError
      var statelessResetErr *quic.StatelessResetError

      return errors.As(err, &appErr) ||
             errors.As(err, &idleErr) ||
             errors.As(err, &statelessResetErr) ||
             errors.Is(err, io.EOF) ||
             errors.Is(err, net.ErrClosed) ||
             strings.Contains(err.Error(), "timeout: no recent network activity")
  }
  ```

- [ ] **Fix `handleQuicConn()` to exit loop on terminal errors** (line 47)
  ```go
  func (n *Nodosum) handleQuicConn(conn *quic.Conn) {
      nodeID := conn.ConnectionState().TLS.ServerName

      for {
          select {
          case <-n.ctx.Done():
              n.logger.Debug("handleQuicConn exiting - context cancelled", "nodeID", nodeID)
              return
          default:
              stream, err := conn.AcceptStream(n.ctx)
              if err != nil {
                  // Check if connection is closed
                  if isConnectionClosed(err) {
                      n.logger.Debug("connection closed, stopping accept loop",
                          "nodeID", nodeID, "error", err.Error())
                      return  // ✅ Exit loop
                  }

                  // Temporary error - log and continue
                  n.logger.Warn("temporary error accepting stream",
                      "nodeID", nodeID, "error", err.Error())
                  continue
              }

              go n.handleStream(stream)
          }
      }
  }
  ```

- [ ] **Change log level from Error to Debug** for expected connection closures

---

## Issue 2: Ready Signal Not Sent

**Symptom**: Sometimes memberlist initiates but `Ready()` timeout occurs - library doesn't signal ready.

**Location**: `internal/nodosum/nodosum.go:196-230`

**Root Cause**: Unknown - needs investigation. Possible causes:
1. Panic in `NotifyJoin()` or other memberlist callback
2. `Start()` called multiple times (closing already-closed channel)
3. Something blocking before `close(n.readyChan)` on line 228

### Investigation Tasks

- [ ] **Add detailed logging in `Start()`** to identify where it's blocking
  ```go
  func (n *Nodosum) Start() error {
      n.logger.Debug("Start: launching QUIC listener")
      n.wg.Go(func() {
          n.listenQuic()
      })

      n.logger.Debug("Start: filtering nodes")
      n.nodeMeta.Lock()
      filteredNodes := n.filterNodes(n.nodeMeta.IPs)
      n.nodeMeta.Unlock()
      n.logger.Debug("Start: filtered nodes", "count", len(filteredNodes), "nodes", filteredNodes)

      n.logger.Debug("Start: calling memberlist.Join()")
      nodesConnected, err := n.ml.Join(filteredNodes)
      if err != nil {
          n.logger.Error("joining initial seed nodes failed", err)
      }
      n.logger.Debug("Start: Join() completed", "nodesConnected", nodesConnected)

      n.logger.Debug("Start: starting nodeAppSyncWorker")
      go n.nodeAppSyncWorker.Start()

      n.logger.Debug("Start: closing readyChan")
      close(n.readyChan)
      n.logger.Debug("Start: completed successfully")

      return nil
  }
  ```

- [ ] **Check logs for panic stack traces** when issue occurs

- [ ] **Verify Start() is only called once** - check application code

### Fix Tasks

- [ ] **Add panic recovery in `NotifyJoin()`** to prevent killing memberlist callbacks
  ```go
  func (d Delegate) NotifyJoin(node *memberlist.Node) {
      defer func() {
          if r := recover(); r != nil {
              d.logger.Error("panic in NotifyJoin recovered", "panic", r, "node", node.Name)
              debug.PrintStack()
          }
      }()

      // ... rest of function
  }
  ```

- [ ] **Add `sync.Once` to prevent double Start()** in nodosum.go
  ```go
  type Nodosum struct {
      startOnce sync.Once
      // ... existing fields
  }

  func (n *Nodosum) Start() error {
      var startErr error
      n.startOnce.Do(func() {
          // ... actual start logic
      })
      return startErr
  }
  ```

- [ ] **Use defer to ensure readyChan always closes** (even on panic)
  ```go
  func (n *Nodosum) Start() error {
      defer func() {
          if r := recover(); r != nil {
              n.logger.Error("panic in Start() recovered", "panic", r)
              close(n.readyChan)  // Still signal (though with error state)
              panic(r)  // Re-panic after cleanup
          }
      }()

      // ... rest of Start logic

      close(n.readyChan)
      return nil
  }
  ```

---

## Issue 3: IO Timeout and Nil Connection Handling

**Symptom**: IO timeout errors when joining seed nodes, followed by potential panics from using nil connections.

**Location**: `internal/nodosum/memberlist.go:15-52`

**Root Cause**: When `Dial()` fails, the code logs the error but continues executing with a nil connection, leading to panics.

### Tasks

- [ ] **Add early return after Dial() error** (line 28-31)
  ```go
  conn, err := d.quicTransport.Dial(d.ctx, addr, d.tlsConfig, d.quicConfig)
  if err != nil {
      d.logger.Error(fmt.Sprintf("error dialing quic connection: %s", err.Error()))
      d.quicConns.RUnlock()  // Don't forget to unlock!
      return  // ✅ Exit early - don't use nil connection
  }
  ```

- [ ] **Fix the RUnlock timing** - currently unlocks at line 22, but should unlock at line 21 OR line 31
  ```go
  func (d Delegate) NotifyJoin(node *memberlist.Node) {
      d.quicConns.RLock()
      if _, exists := d.quicConns.conns[node.Name]; exists {
          d.quicConns.RUnlock()
          d.logger.Debug(fmt.Sprintf("quic connection exists: %s", node.Name))
          return
      }
      d.quicConns.RUnlock()  // ✅ Good - unlocked after check

      // Dial without holding lock
      addr, err := net.ResolveUDPAddr("udp", node.Addr.String()+":"+strconv.Itoa(d.quicPort))
      if err != nil {
          d.logger.Error(fmt.Sprintf("error resolving quic address: %s", err.Error()))
          return  // ✅ Early return
      }

      conn, err := d.quicTransport.Dial(d.ctx, addr, d.tlsConfig, d.quicConfig)
      if err != nil {
          d.logger.Error(fmt.Sprintf("error dialing quic connection: %s", err.Error()))
          return  // ✅ Early return - don't use nil conn
      }

      // ... rest of function
  }
  ```

- [ ] **Add nil check before CloseWithError** (line 37)
  ```go
  if conn != nil {
      err = conn.CloseWithError(0, "duplicate quic connection")
      if err != nil {
          d.logger.Error(fmt.Sprintf("error closing duplicate quic connection: %s", err.Error()))
      }
  }
  ```

- [ ] **Optional: Add dial timeout context** to prevent hanging indefinitely
  ```go
  dialCtx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
  defer cancel()

  conn, err := d.quicTransport.Dial(dialCtx, addr, d.tlsConfig, d.quicConfig)
  ```

- [ ] **Add logging for successful connection establishment**
  ```go
  d.logger.Info("quic connection established",
      "node", node.Name,
      "remoteAddr", conn.RemoteAddr().String(),
      "serverName", conn.ConnectionState().TLS.ServerName)
  ```

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
