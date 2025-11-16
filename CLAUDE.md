# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Mycorrizal is an embedded library for building scalable, modern, and efficient modular monoliths in Go. It provides distributed system capabilities including messaging, caching, and coordination without requiring external infrastructure.

**Key Metaphor**: The library uses fungal network terminology - each service instance is like a mushroom/tree, and Mycorrizal provides the mycelium (underground network) that connects them.

## Commands

### Testing
```bash
go test -v                    # Run all tests
go test -v ./internal/nodosum # Run tests for specific package
go test -v ./internal/mycel   # Run cache layer tests
```

### Building
```bash
go build                      # Build the library
go build ./cmd/pulse.go       # Build the Pulse CLI (not yet implemented)
```

### Code Generation
```bash
make generate                 # Generate protobuf files (currently commented out)
```

## Architecture

### Component Layer Architecture

The system is organized into distinct layers, each with specific responsibilities:

#### 1. Nodosum - Coordination and Consensus Layer
**Location**: `internal/nodosum/`

The cluster management and communication layer. Handles:
- Node discovery via DNS-SD, Consul API, or static configuration
- Star topology network formation (all nodes connect to all nodes)
- Connection lifecycle management with TCP listeners and UDP discovery
- Application-level multiplexing over cluster connections
- ACL-based authentication using shared secrets
- Optional TLS for cluster communication

**Key files**:
- `nodosum.go`: Core orchestration and lifecycle management
- `mux.go`: Connection multiplexer routing packets between applications and connections
- `application.go`: Application interface for cluster messaging
- `protocol.go`: Low-level packet encoding/decoding
- `conn.go`: Individual node connection management
- `acl.go`: Authentication and authorization
- `discoverDns.go`, `discoverConsul.go`: Service discovery implementations

**Important patterns**:
- Applications register with unique identifiers to send/receive messages across the cluster
- The multiplexer routes packets based on application ID in frame headers
- Each application gets dedicated send/receive workers for concurrent message handling
- Uses `github.com/conamu/go-worker` for structured concurrent task execution

#### 2. Mycel - Caching and Storage Layer
**Location**: `internal/mycel/`

Distributed cache implementation using LRU and TTL-based eviction:
- Local doubly-linked list (DLL) based LRU cache per node
- Bucket-based namespacing with configurable TTL and max size
- Rendezvous hashing for deterministic cache key placement across nodes
- Concurrent TTL eviction via background workers

**Key files**:
- `mycel.go`: Component initialization and lifecycle
- `cache.go`: Public Cache interface (Get, Put, Delete, CreateBucket, PutTtl)
- `ds.go`: Data structures including LRU DLL, buckets, and rendezvous hashing
- `task.go`: TTL eviction worker implementation

**Cache architecture**:
- Each node maintains its own LRU cache with local eviction
- Rendezvous hashing calculates primary + N replica nodes for each key
- KeyVal hashmap provides O(1) lookups; DLL maintains LRU ordering
- TTL eviction runs periodically via worker tasks

#### 3. Top-Level Mycorrizal Interface
**Location**: Root `mycorrizal.go`, `config.go`

Unified API that orchestrates both Nodosum and Mycel:
```go
type Mycorrizal interface {
    Start() error
    Shutdown() error
    RegisterApplication(uniqueIdentifier string) nodosum.Application
    GetApplication(uniqueIdentifier string) nodosum.Application
    Cache() mycel.Cache
}
```

**Configuration modes**:
- `DC_MODE_DNS_SD`: DNS service discovery
- `DC_MODE_CONSUL`: Consul API discovery
- `DC_MODE_STATIC`: Static node address list
- `SingleMode`: Disables clustering, enables CLI-only mode

**Environment variables**:
- `MYCORRIZAL_ID`: Optional node ID (auto-generates UUID if not set)

### Startup and Lifecycle

**Initialization flow**:
1. Parse Config (discovery mode, TLS settings, timeouts, etc.)
2. Create Nodosum instance (cluster layer)
3. Create Mycel instance (cache layer) - depends on Nodosum
4. Call `Start()` which launches both components in parallel
5. Wait for `Ready()` signals (30s timeout default)

**Startup sequence**:
- Nodosum starts TCP listener, UDP discovery, multiplexer workers
- Sends UDP HELLO packets to discover peers
- Mycel waits for Nodosum ready signal
- Registers "SYSTEM-MYCEL" application for cache synchronization
- Starts TTL eviction workers

**Shutdown**:
- Gracefully closes all connections
- Cancels context to stop all goroutines
- Waits on WaitGroup for cleanup
- Both layers shut down in parallel

### Discovery and Clustering

**Node discovery mechanisms**:
1. **Static mode**: Configured list in `Config.NodeAddrs`
2. **DNS-SD**: Queries DNS service records from `Config.DiscoveryHost`
3. **Consul**: Polls Consul API at `Config.DiscoveryHost` with optional mTLS

**Connection establishment**:
- UDP HELLO/HELLO_ACK handshake with shared secret authentication
- TCP connection with protocol version negotiation
- Optional TLS upgrade with certificate validation
- ACL challenge/response using SHA256 HMAC

### Application Communication Pattern

Applications use Nodosum for cluster-wide messaging:

```go
app := mycorrizal.RegisterApplication("my-service")
app.SetReceiveFunc(func(payload []byte) error {
    // Handle incoming messages
})
app.Send(data, []string{"node-id-1", "node-id-2"}) // Send to specific nodes
app.Send(data, []string{}) // Broadcast to all nodes
```

**Frame structure**:
- Version (1 byte)
- Frame type (1 byte)
- Length (4 bytes)
- Application ID length (2 bytes)
- Application ID (variable)
- Payload (variable)

### Cache Usage Pattern

```go
cache := mycorrizal.Cache()
cache.CreateBucket("sessions", 15*time.Minute, 10000)
cache.Put("sessions", "user:123", userData, 0) // Use bucket default TTL
cache.Get("sessions", "user:123")
cache.PutTtl("sessions", "user:123", 30*time.Minute) // Extend TTL
cache.Delete("sessions", "user:123")
```

## Development Notes

### Testing Considerations

- Tests require valid discovery configuration or will fail
- Use `GetDefaultConfig()` for basic test setups with `DC_MODE_STATIC`
- Most components depend on proper context and WaitGroup setup
- TLS tests need valid certificate pools and certificates

### Known Issues / TODOs

From code comments:
- Cytoplasm (event bus) layer not yet implemented
- Hypha (node integration) layer not yet implemented
- Pulse CLI exists but functionality not implemented
- Cache Delete operation could be optimized using KV map for O(1) node lookup
- Network topology improvements planned (cohorts forming small stars)
- Service advertisement/registration planned
- Leader election for persistent storage to S3 planned

### Common Patterns

**Worker usage**: The codebase extensively uses `github.com/conamu/go-worker` for structured concurrency:
```go
worker := worker.NewWorker(ctx, "worker-name", wg, taskFunc, logger, interval)
worker.InputChan = make(chan any, bufferSize)
worker.OutputChan = existingChannel // Optional
go worker.Start()
```

**Sync patterns**: Heavy use of `sync.Map`, `sync.RWMutex`, and per-struct locking for concurrent access

**Context cancellation**: All long-running operations respect context cancellation for graceful shutdown

### Dependencies

- `github.com/conamu/go-worker`: Structured concurrent task execution (local replace in go.mod)
- `github.com/google/uuid`: Node ID generation

## Project Status

Work in progress - not yet accepting PRs. Core cluster coordination (Nodosum) and local caching (Mycel) are functional. Event bus (Cytoplasm) and CLI (Pulse) layers are planned but not implemented.