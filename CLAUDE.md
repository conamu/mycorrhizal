# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Mycorrizal is an embedded library for building scalable, modern, and efficient modular monoliths in Go. It provides distributed system capabilities including messaging, caching, and coordination without requiring external infrastructure.

**Key Metaphor**: The library uses fungal network terminology - each service instance is like a mushroom/tree, and Mycorrizal provides the mycelium (underground network) that connects them.

## Commands

### Building
```bash
go build                      # Build the library
go build ./cmd/pulse.go       # Build the Pulse CLI (skeleton only)
```

### Code Generation
```bash
make generate                 # Generate protobuf files (currently commented out)
```

**Note**: No test files exist yet. The project has no `*_test.go` files.

## Architecture

### Component Layer Architecture

```
Mycorrizal (top-level API)
  ├── Nodosum (cluster coordination)
  │   ├── Memberlist (gossip-based membership via hashicorp/memberlist)
  │   ├── QUIC Transport (mTLS, per-app streams)
  │   └── Applications (registered services)
  │       └── SYSTEM-MYCEL (built-in cache coordination app)
  │
  └── Mycel (cache layer)
      ├── Local LRU caches (per-bucket DLL)
      ├── Rendezvous hashing (primary node selection)
      ├── Remote operations via SYSTEM-MYCEL app
      └── TTL eviction worker (every 10s)
```

#### 1. Nodosum - Coordination and Consensus Layer
**Location**: `internal/nodosum/`

Cluster management using **gossip protocol** (hashicorp/memberlist) for membership and **QUIC** for application data transport.

**Key files**:
- `nodosum.go`: Core orchestration, startup (QUIC listener + memberlist.Join), shutdown (memberlist.Leave + context cancel)
- `memberlist.go`: Delegate implementing NotifyJoin/Leave/Update/Alive/Merge - triggers QUIC connection establishment/teardown
- `conn.go`: QUIC connection/stream handling - listenQuic(), handleQuicConn(), handleStream(), streamReadLoop()
- `registry.go`: NodeMetaMap (node registry), getOrOpenQuicStream() for lazy stream creation
- `quic.go`: Frame encode/decode functions, frame type constants, getConnectedNodes()
- `application.go`: Application interface and implementation - Send(), Request(), SetReceiveFunc(), SetRequestHandler()
- `cert.go`: Auto-generates per-node TLS certificates signed by CA (from 1Password or direct config)
- `config.go`: Nodosum-specific config struct
- `discoverDns.go`, `discoverConsul.go`: Discovery stubs (not implemented)

**Application interface**:
```go
type Application interface {
    Send(payload []byte, ids []string) error                                         // Broadcast or targeted send
    Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error) // Request/response pattern
    SetReceiveFunc(func(payload []byte) error)                                        // Handle incoming data frames
    SetRequestHandler(func(payload []byte, senderId string) (responsePayload []byte, err error)) // Handle request frames
    Nodes() []string                                                                  // Connected node IDs
}
```

**Membership flow**:
1. Memberlist gossip handles node discovery and health checking
2. Delegate callbacks (NotifyJoin/Leave/Alive) manage QUIC connections
3. Node metadata carries QUIC port (2 bytes, big-endian uint16)
4. QUIC connections use mTLS with auto-generated per-node certs (CN = nodeId)
5. Exponential backoff retry on failed QUIC dials (max 5 attempts)

**Stream management**:
- Streams are lazily created per (nodeId, appId, streamName) triple
- Key format in registry: `"nodeId:appId:streamName"`
- Each stream starts with a STREAM_INIT frame, then carries DATA/REQUEST/RESPONSE frames
- Both incoming and outgoing streams get a read loop goroutine

#### 2. Mycel - Caching and Storage Layer
**Location**: `internal/mycel/`

**Key files**:
- `mycel.go`: Lifecycle (New, Start waits for Nodosum ready, registers SYSTEM-MYCEL app)
- `cache.go`: Public Cache interface - Get, Set, Delete, SetTtl, CreateBucket; isLocal() routing
- `local.go`: Local cache operations (getLocal, setLocal, deleteLocal, setTtlLocal)
- `remote.go`: Remote cache operations via Request/Response over SYSTEM-MYCEL app
- `ds.go`: Data structures - LRU DLL (lruBucket with Push/Delete), keyVal hashmap, rendezvous hashing (score/getReplicas)
- `encoding.go`: GOB encoding for remote cache payloads (remoteCachePayload struct)
- `task.go`: TTL eviction worker (iterates all buckets, removes expired nodes)
- `rebalancer.go`: Stub for cache rebalancing on topology changes
- `config.go`: Mycel-specific config

**Cache interface**:
```go
type Cache interface {
    CreateBucket(name string, ttl time.Duration, maxLen int) error
    Get(bucket, key string) (any, error)
    Set(bucket, key string, value any, ttl time.Duration) error
    Delete(bucket, key string) error
    SetTtl(bucket, key string, ttl time.Duration) error
}
```

**Data flow**:
- Every operation checks `isLocal(bucket, key)` using rendezvous hashing
- Local: direct pointer access to DLL nodes via keyVal hashmap
- Remote: GOB-encoded request via `app.Request()` to primary node, decoded and executed there
- Remote operations: GET (0x00), SET (0x01), SETTTL (0x02), DELETE (0x03), RESPONSE (0x04)

**Cache data structures**:
- `keyVal`: `map[bucket+key] -> *node` for O(1) lookups
- `lruBuckets`: `map[bucket] -> *lruBucket` (each bucket has its own DLL)
- `nodeScoreHashMap`: `map[bucket+key] -> nodeId` caches primary replica calculation
- `node`: DLL node with key, data (any), expiresAt, next/prev pointers
- Rendezvous hash: `SHA256(key || nodeId)` -> uint64 score, highest score = primary

#### 3. Top-Level Mycorrizal Interface
**Location**: Root `mycorrizal.go`, `config.go`

```go
type Mycorrizal interface {
    Start() error
    Ready(timeout time.Duration) error
    Shutdown() error
    RegisterApplication(uniqueIdentifier string) nodosum.Application
    GetApplication(uniqueIdentifier string) nodosum.Application
    Cache() mycel.Cache
}
```

### QUIC Frame Protocol

Four frame types over QUIC streams (constants in `quic.go`):

**STREAM_INIT** (0x01) - First frame on any new stream:
```
[1 byte: type=0x01]
[2 bytes: nodeID length (big-endian uint16)]
[N bytes: nodeID (UTF-8)]
[2 bytes: appID length (big-endian uint16)]
[N bytes: appID (UTF-8)]
[2 bytes: streamName length (big-endian uint16)]
[N bytes: streamName (UTF-8)]
```

**DATA** (0x02) - Fire-and-forget application payload:
```
[1 byte: type=0x02]
[4 bytes: payload length (big-endian uint32)]
[N bytes: payload]
```

**REQUEST** (0x03) - Request expecting a RESPONSE:
```
[1 byte: type=0x03]
[2 bytes: correlationID length (big-endian uint16)]
[N bytes: correlationID (UTF-8, UUID)]
[4 bytes: payload length (big-endian uint32)]
[N bytes: payload]
```

**RESPONSE** (0x04) - Response to a REQUEST:
```
[1 byte: type=0x04]
[2 bytes: correlationID length (big-endian uint16)]
[N bytes: correlationID (UTF-8)]
[2 bytes: error length (big-endian uint16)]
[N bytes: error (UTF-8, empty if no error)]
[4 bytes: payload length (big-endian uint32)]
[N bytes: payload]
```

### Configuration

**Top-level Config** (`config.go`):

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Ctx` | `context.Context` | `context.Background()` | Parent context |
| `Logger` | `*slog.Logger` | Text handler, Info level | Structured logger |
| `DiscoveryMode` | `int` | `DC_MODE_STATIC` | DC_MODE_DNS_SD / DC_MODE_CONSUL / DC_MODE_STATIC |
| `DiscoveryHost` | `*url.URL` | nil | Required for DNS-SD and Consul modes |
| `NodeAddrs` | `[]net.TCPAddr` | `[]` | Required for static mode |
| `SingleMode` | `bool` | false | Disables clustering |
| `ListenPort` | `int` | 6969 | Memberlist bind port |
| `QuicPort` | `int` | 0 | QUIC transport port |
| `SharedSecret` | `string` | "" | Memberlist encryption key |
| `HandshakeTimeout` | `time.Duration` | 2s | Connection handshake timeout |
| `CacheReplicaCount` | `int` | 3 | Configured but replication not yet implemented |
| `CacheRemoteTimeout` | `time.Duration` | 100ms | Remote cache operation timeout |
| `MemberlistConfig` | `*memberlist.Config` | `DefaultLocalConfig()` | Hashicorp memberlist config |
| `Debug` | `bool` | false | Enables pprof on localhost:6060 |
| `ClusterTLSCACert` | `*x509.Certificate` | nil | CA cert for node cert generation |
| `CaCert` / `CaKey` | cert/key | nil | Direct CA (higher priority than 1Password) |
| `OnePassToken` | `string` | "" | 1Password service account token for CA retrieval |
| `HttpClientTLSEnabled` | `bool` | false | TLS for Consul HTTP client |

**Environment variables**:
- `MYCORRIZAL_ID`: Custom node ID (auto-generates UUID if not set)

**Default config helpers**: `GetDefaultClusterConfig()`, `GetDefaultSingleConfig()`

### Startup and Lifecycle

**Startup sequence** (`mycorrizal.Start()`):
1. Optional: start pprof debug server on localhost:6060
2. Start Nodosum (in goroutine):
   - Start QUIC listener on configured port
   - Filter self from node addresses
   - `memberlist.Join()` to seed nodes
   - Trigger `NotifyJoin()` for initial members (QUIC connection establishment)
   - Close readyChan
3. Start Mycel (in goroutine):
   - Wait for Nodosum ready (30s timeout)
   - Start TTL eviction worker (10s interval)
   - Close readyChan
4. Wait for both to complete, close top-level readyChan

**Shutdown** (`mycorrizal.Shutdown()`):
- Nodosum: `memberlist.Leave(30s)` -> cancel context -> close UDP conn -> wait on WaitGroup
- Mycel: cancel context -> wait on WaitGroup
- Both run in parallel

### Application Communication Pattern

```go
// Register and configure
app := mycorrizal.RegisterApplication("my-service")
app.SetReceiveFunc(func(payload []byte) error {
    // Handle fire-and-forget DATA frames
    return nil
})
app.SetRequestHandler(func(payload []byte, senderId string) ([]byte, error) {
    // Handle REQUEST frames, return response
    return responseData, nil
})

// Send data
app.Send(data, []string{"node-id-1"})  // Targeted
app.Send(data, []string{})             // Broadcast to all nodes

// Request/response
resp, err := app.Request(data, "target-node-id", 5*time.Second)
```

### Cache Usage Pattern

```go
cache := mycorrizal.Cache()
cache.CreateBucket("sessions", 15*time.Minute, 10000)
cache.Set("sessions", "user:123", userData, 0)            // Use bucket default TTL
val, err := cache.Get("sessions", "user:123")
cache.SetTtl("sessions", "user:123", 30*time.Minute)      // Extend TTL
cache.Delete("sessions", "user:123")
```

## Development Notes

### Dependencies

| Dependency | Version | Purpose |
|-----------|---------|---------|
| `github.com/conamu/go-worker` | local replace | Structured concurrent task execution |
| `github.com/hashicorp/memberlist` | v0.5.4 | Gossip-based cluster membership |
| `github.com/quic-go/quic-go` | v0.59.0 | QUIC transport for application data |
| `github.com/1password/onepassword-sdk-go` | v0.4.0 | CA certificate retrieval from 1Password |
| `github.com/google/uuid` | v1.6.0 | Node ID and correlation ID generation |

**Go version**: 1.26.0

### Common Patterns

**Worker usage** (`github.com/conamu/go-worker`):
```go
worker := worker.NewWorker(ctx, "worker-name", wg, taskFunc, logger, interval)
worker.InputChan = make(chan any, bufferSize)
go worker.Start()
```

**Sync patterns**: `sync.RWMutex` embedded in wrapper structs (keyVal, lruBuckets, quicConns, etc.), `sync.Mutex` for NodeMetaMap

**Context cancellation**: All long-running operations (QUIC listener, stream read loops, workers) respect context cancellation

**TLS**: Always enforced for QUIC connections. Per-node certificates auto-generated from CA at startup. CA sourced from direct config (CaCert/CaKey) or 1Password.

### Known Issues / TODOs

- Cache replication not implemented (CacheReplicaCount configured but only primary stores data)
- Rebalancer is a stub (rebalancer.go exists but empty)
- DNS-SD and Consul discovery not implemented (empty stubs)
- Cytoplasm (event bus) layer not yet implemented
- Hypha (node integration) layer not yet implemented
- Pulse CLI is a skeleton (hardcoded localhost:6969)
- Delete operation iterates DLL instead of using KV map for O(1) node lookup
- Message drops after 100ms if application receive channel full (no backpressure)
- Unbounded maps: quicApplicationStreams.streams, pendingRequests, dialAttempts
- See `PRODUCTION_READINESS.md` for full production readiness assessment

### Documentation Files

| File | Contents |
|------|----------|
| `docs/ARCHITECTURE_HYBRID_TOPOLOGY.md` | Hierarchical topology design |
| `docs/QUIC_MIGRATION_PLAN.md` | QUIC implementation strategy |
| `docs/STREAM_STRATEGY.md` | Stream multiplexing details |
| `docs/CERTIFICATE_GENERATION.md` | TLS/cert setup |
| `docs/ONEPASSWORD_INTEGRATION.md` | 1Password cert management |
| `docs/REVIEW.md` | Code review notes |
| `PRODUCTION_READINESS.md` | Production readiness checklist |
| `CACHE_REBALANCING_STRATEGY.md` | Replica-aware rebalancing design |
| `MEMBERLIST_DELEGATE.md` | Delegate implementation notes |
| `QUIC_LOOPBACK_BUG_ANALYSIS.md` | Self-connection bug analysis |

## Project Status

Work in progress - not yet accepting PRs. Core cluster coordination (Nodosum) and distributed caching (Mycel) are functional. Major transport migration from TCP/UDP to gossip + QUIC completed. Next priority: cache rebalancer implementation. Event bus (Cytoplasm) and CLI (Pulse) layers are planned but not implemented.
