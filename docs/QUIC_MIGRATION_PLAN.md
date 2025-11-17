# Nodosum Architecture Migration Plan: Gossip + QUIC

**Status**: Research Phase
**Last Updated**: 2025-11-17
**Author**: Architecture Review

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Architecture Comparison](#architecture-comparison)
3. [Benefits Analysis](#benefits-analysis)
4. [Current Architecture Overview](#current-architecture-overview)
5. [Target Hybrid Architecture: Gossip + QUIC](#target-hybrid-architecture-gossip--quic)
6. [Gossip Protocol Integration](#gossip-protocol-integration)
7. [QUIC Transport Layer](#quic-transport-layer)
8. [Migration Strategy](#migration-strategy)
9. [Detailed Code Changes](#detailed-code-changes)
10. [Testing & Validation](#testing--validation)
11. [Performance Benchmarking](#performance-benchmarking)
12. [Rollout Plan](#rollout-plan)
13. [Risk Assessment](#risk-assessment)
14. [References](#references)

---

## Executive Summary

This document outlines a plan to migrate Mycorrizal's Nodosum networking layer from the current custom TCP/UDP implementation to a modern **hybrid architecture using Gossip protocol (Memberlist) for membership management and QUIC for application data transport**. This migration aligns with project goals for improved resilience, performance, and scalability while maintaining secure communication.

### Key Points

- **Migration Type**: Hybrid architecture (Gossip + QUIC) - not backward compatible
- **Estimated Effort**: 6 weeks for experienced Go developer
- **Primary Libraries**:
  - `github.com/hashicorp/memberlist` - Production-proven gossip protocol (SWIM)
  - `github.com/quic-go/quic-go` - Modern QUIC transport
- **Code Reduction**: ~900 lines eliminated (~73% of Nodosum), net reduction ~32%
- **Impact to Higher Layers**: None - Application interface remains unchanged
- **Primary Benefits**:
  - Production-grade failure detection (SWIM protocol)
  - Native stream multiplexing (QUIC)
  - Mandatory encryption (TLS 1.3)
  - Better observability (metrics, events)
  - Reduced operational risk

### Recommendation

**Strongly recommend proceeding with Gossip + QUIC hybrid architecture** - This approach replaces custom, unproven networking code with battle-tested libraries used in production systems like Consul, Nomad, and Grafana Mimir. It addresses critical production gaps in the current implementation (no active health checks, no split-brain protection, limited observability) while significantly reducing maintenance burden.

---

## Architecture Comparison

### Three Architecture Options

| Aspect | Current (TCP/UDP) | QUIC-Only | **Gossip + QUIC (Recommended)** |
|--------|-------------------|-----------|----------------------------------|
| **Membership Management** | Custom UDP handshake | QUIC handshake | HashiCorp Memberlist (SWIM) |
| **Failure Detection** | Passive (TCP breaks) | Passive (QUIC breaks) | **Active probes + suspicion** |
| **Application Data** | TCP with custom framing | QUIC streams | **QUIC streams** |
| **Head-of-line Blocking** | Yes (single TCP conn) | No (QUIC streams) | **No (QUIC streams)** |
| **Encryption** | Optional TLS | Mandatory TLS 1.3 | **Mandatory TLS 1.3** |
| **Split-brain Protection** | None | None | **Gossip detects partitions** |
| **Observability** | Logs only | QUIC metrics | **Gossip + QUIC metrics** |
| **Production Maturity** | Alpha/POC | Good | **Excellent (battle-tested)** |
| **Lines of Code** | ~1,500 custom | ~1,100 custom | **~600 custom + proven libraries** |
| **Development Effort** | N/A (current) | 4 weeks | **6 weeks** |

### Why Hybrid (Gossip + QUIC) is Superior

**Separation of Concerns**:
- **UDP Gossip**: Optimized for membership (connectionless, unreliable probes, eventual consistency)
- **QUIC**: Optimized for data (reliable streams, multiplexing, low latency)

**Production Readiness**:
- Memberlist used in 1,699+ Go packages (Consul, Nomad, Grafana Mimir)
- SWIM protocol: industry-standard failure detection
- Proven at scale (thousands of nodes in production deployments)

**Code Quality**:
- Eliminate ~900 lines of custom networking logic
- Keep only domain-specific application multiplexing
- Leverage decades of distributed systems research

---

## Benefits Analysis

### 1. Fault Tolerance & Resilience (MAJOR IMPROVEMENT)

| Feature | Current (TCP/UDP) | Gossip + QUIC | Benefit |
|---------|-------------------|---------------|---------|
| **Failure Detection** | Passive (TCP break only) | **Active SWIM probes** | Detect failures in ~1-5 seconds vs. minutes |
| **False Positive Rate** | High (no suspicion) | **Low (suspect → dead)** | Avoid unnecessary reconnections |
| **Split-brain Detection** | None | **Gossip propagates partitions** | Cluster aware of network splits |
| **Health Check Frequency** | Never | **Configurable (default 1s)** | Continuous cluster health monitoring |
| **Failure Propagation** | O(n²) connections must break | **O(log n) gossip rounds** | Fast cluster-wide awareness |
| Connection Migration | Not supported | **QUIC native support** | Nodes survive IP/port changes (K8s pods) |
| Stream Isolation | Single TCP connection | **Independent QUIC streams** | One app failure doesn't kill others |
| Reconnection Speed | Full TCP handshake + TLS | **0-RTT resumption** | ~50-80ms faster reconnects |
| Partial Network Failure | Connection drops | **Indirect probes** | Route around intermediate failures |

### 2. Performance & Scalability

| Metric | Current | Gossip + QUIC | Improvement |
|--------|---------|---------------|-------------|
| **Head-of-line Blocking** | Yes (single TCP) | **No (QUIC streams)** | Multi-app throughput ↑30-50% |
| **Multiplexing Overhead** | Channel-based routing | **Native QUIC streams** | CPU usage ↓10-20% |
| **Connection Setup** | UDP + TCP + optional TLS | **Gossip join + QUIC** | Latency ↓30-50% |
| **Cluster Formation** | O(n²) handshakes | **O(n) gossip + QUIC dials** | Faster cluster bootstrap |
| **Membership Updates** | Periodic sync (3s) | **Event-driven callbacks** | Real-time cluster awareness |
| **Flow Control** | Channel backpressure only | **Per-stream + connection** | Better congestion handling |
| **Tested Cluster Size** | Unknown | **1000s of nodes** | Proven scalability |
| **Congestion Control** | Standard TCP | **Modern QUIC (BBR)** | Better performance on lossy networks |

### 3. Security Enhancements

| Aspect | Current | Gossip + QUIC | Improvement |
|--------|---------|---------------|-------------|
| **UDP Handshake** | Plaintext 64-byte secret | **Memberlist AES-GCM** | No credential exposure |
| **Gossip Messages** | N/A | **Encrypted** | All cluster communication secure |
| **Application Data** | Optional TLS 1.2/1.3 | **Mandatory TLS 1.3 (QUIC)** | Always secure, modern crypto |
| **Key Rotation** | Manual restart required | **Memberlist supports rotation** | Zero-downtime security updates |
| **Authentication** | Shared secret | **Gossip secret + mTLS certs** | Defense in depth |
| **Connection Tokens** | Custom one-time tokens | **QUIC address validation** | Standards-based security |

### 4. Operational & Observability Improvements (CRITICAL GAIN)

| Aspect | Current | Gossip + QUIC | Improvement |
|--------|---------|---------------|-------------|
| **Metrics** | None | **Prometheus built-in** | Full observability |
| **Node State** | Binary (alive/dead) | **Alive/Suspect/Dead** | Nuanced health tracking |
| **Event Notifications** | None | **Join/Leave/Update callbacks** | React to cluster changes |
| **Cluster Size Visibility** | Manual tracking | **memberlist.NumMembers()** | Real-time cluster size |
| **Debugging** | Logs only | **Metrics + Events + qlog/qvis** | Rich diagnostic tools |
| **Health Endpoints** | None | **memberlist.GetHealthScore()** | Automated monitoring |
| **Production Use** | Unknown | **Consul, Nomad, Mimir** | Battle-tested reliability |
| **Documentation** | README + comments | **Extensive community docs** | Easier troubleshooting |
| **Community Support** | Solo developer | **HashiCorp + community** | Faster issue resolution |

### 5. Code Maintenance & Quality

| Aspect | Current | Gossip + QUIC | Improvement |
|--------|---------|---------------|-------------|
| **Custom Networking Code** | ~1,500 lines | **~600 lines** | 60% reduction |
| **Test Coverage** | ~8% (98/1235 lines) | **Extensive (library tests)** | Higher confidence |
| **Protocol Complexity** | Custom UDP/TCP/framing | **Standard gossip + QUIC** | Easier to understand |
| **Bug Surface** | All custom code | **Mostly battle-tested libs** | Fewer bugs |
| **Maintenance Burden** | High (all custom) | **Low (leverage libraries)** | Focus on business logic |
| **Future Features** | Must implement from scratch | **Library provides many** | Faster development |

---

## Current Architecture Overview

### Network Topology

```
Node A                          Node B
┌─────────────────┐            ┌─────────────────┐
│   UDP Listener  │◄──HELLO───►│   UDP Listener  │
│  (port 7946)    │────ACK─────│  (port 7946)    │
│                 │──CONN+tok──│                 │
└────────┬────────┘            └────────┬────────┘
         │                              │
┌────────▼────────┐            ┌────────▼────────┐
│   TCP Listener  │◄───────────►│   TCP Listener  │
│  (port 7946)    │ Persistent  │  (port 7946)    │
│                 │ Connection  │                 │
└────────┬────────┘            └────────┬────────┘
         │                              │
┌────────▼────────────────────────────────────────┐
│          Application Multiplexer                │
│  (Routes messages via ApplicationID)            │
└─────────────────────────────────────────────────┘
```

### Current Protocol Stack

1. **UDP Layer** (`listenUdp`, `handleUdpPacket`)
   - Discovery and handshake only
   - 3-way handshake: HELLO → HELLO_ACK → HELLO_CONN
   - ConnInit tie-breaking determines client/server roles
   - One-time token generation for TCP authentication
   - **Security issue**: 64-byte shared secret sent in plaintext

2. **TCP Layer** (`listenTcp`, `serverHandshake`)
   - Long-lived persistent connections
   - Optional TLS upgrade after connection
   - Token verification from UDP handshake
   - Single connection per node pair (star topology)
   - Custom framing protocol ("Glutamate")

3. **Framing Protocol** (`protocol.go`)
   ```
   [9 bytes fixed header]
   | Version (1) | Type (1) | Flag (1) | Length (4) | AppIDLen (2) |
   [Variable header]
   | ApplicationID (variable UTF-8 string) |
   [Payload]
   | Payload (Length bytes) |
   ```

4. **Multiplexing** (`mux.go`)
   - N workers process inbound frames from `globalReadChannel`
   - N workers process outbound frames to `globalWriteChannel`
   - Application lookup via `sync.Map`
   - Connection lookup via `sync.Map`
   - Channel-based backpressure (100-item buffers)

### Connection Lifecycle

```
1. Startup
   ├─ Start TCP listener (port 7946)
   ├─ Start UDP listener (port 7946)
   ├─ Start multiplexer workers
   └─ Broadcast UDP HELLO to known peers

2. Peer Discovery (UDP handshake)
   ├─ Send HELLO (ConnInit + NodeID + Secret)
   ├─ Receive HELLO_ACK (remote ConnInit)
   ├─ Compare ConnInit values
   │  ├─ Lower value: Send HELLO_CONN with token, dial TCP
   │  └─ Higher value: Wait for TCP connection
   └─ TCP connection established

3. TCP Connection Setup
   ├─ Server: Send CONN_INIT frame, wait for token
   ├─ Client: Receive CONN_INIT, send token
   ├─ Verify token matches UDP handshake
   └─ Start read/write loops

4. Steady State
   ├─ Apps send via globalWriteChannel
   ├─ Multiplexer creates frames
   ├─ Write loop sends to TCP connection
   ├─ Read loop receives from TCP connection
   ├─ Multiplexer routes to application
   └─ App receive callback invoked

5. Shutdown
   ├─ Cancel connection context
   ├─ Close TCP connection
   ├─ Wait for read/write goroutines
   └─ Mark node as not alive
```

### Key Files

| File | Lines | Responsibility |
|------|-------|----------------|
| `nodosum.go` | 397 | Core orchestration, lifecycle, listeners |
| `conn.go` | 195 | Connection management, read/write loops |
| `protocol.go` | 370 | Frame encoding/decoding, UDP packets |
| `mux.go` | 127 | Multiplexer workers, routing logic |
| `application.go` | 126 | Application interface, send/receive workers |
| `registry.go` | 84 | Connection and application registries |
| `acl.go` | 66 | Authentication (minimal implementation) |
| `discover*.go` | ~100 | DNS-SD, Consul, static discovery |

**Total Nodosum LOC**: ~1,500 lines

---

## Target Hybrid Architecture: Gossip + QUIC

### Network Topology with Gossip + QUIC

```
┌─────────────────────────────────────────────────────────┐
│                    Mycorrizal Node                      │
├─────────────────────────────────────────────────────────┤
│  Application Layer                                      │
│  ├── Mycel Cache                                        │
│  ├── Future: Cytoplasm Event Bus                        │
│  └── Custom Applications                                │
├─────────────────────────────────────────────────────────┤
│  Nodosum Coordination Layer                             │
│  ┌──────────────────────────┬──────────────────────┐   │
│  │ Memberlist (UDP :6969)   │ QUIC Transport       │   │
│  │ - SWIM failure detection │ (:6970)              │   │
│  │ - Membership tracking    │ - App data streams   │   │
│  │ - Node metadata (512B)   │ - Cache sync         │   │
│  │ - Health probes          │ - Per-app streams    │   │
│  │ - AES-GCM encryption     │ - TLS 1.3 encrypted  │   │
│  └──────────────────────────┴──────────────────────┘   │
├─────────────────────────────────────────────────────────┤
│  Network Layer                                          │
│  ├── UDP :6969 (Gossip membership)                      │
│  └── QUIC :6970 (Application data over UDP)             │
└─────────────────────────────────────────────────────────┘
```

### Communication Flow

**Node Join Sequence:**
```
1. New node starts
   ├─ Memberlist joins cluster (UDP gossip)
   │  ├─ Sends join request to seed nodes
   │  ├─ Receives member list
   │  └─ Begins SWIM probes
   │
   ├─ On NodeJoin callback:
   │  └─ Dial QUIC connection to new member
   │
   └─ Applications can now send/receive data
```

**Steady-State Operation:**
```
Gossip Layer (UDP)          QUIC Layer (over UDP)
     │                             │
     ├─ Probe A → B               ├─ App data A → B (stream 1)
     ├─ Probe B → C               ├─ Cache sync A → B (stream 2)
     ├─ Probe C → A               ├─ App data B → C (stream 1)
     │                             │
     ├─ Gossip membership          ├─ Reliable delivery
     ├─ Detect failures            ├─ Flow control
     └─ Propagate events           └─ Congestion control
```

**Failure Detection:**
```
Node A detects B might be down:
1. Memberlist: A fails to probe B
2. Memberlist: A marks B as "Suspect"
3. Memberlist: A asks C to probe B (indirect)
4. If C also fails: B marked "Dead"
5. NodeLeave callback triggered
6. Nodosum closes QUIC connection to B
7. Applications notified (Nodes() returns updated list)
```

### Node-to-Node Full Architecture

```
Node A                                Node B
┌───────────────────────────────┐    ┌───────────────────────────────┐
│ Memberlist (:6969)            │    │ Memberlist (:6969)            │
│  UDP Gossip                   │◄──►│  UDP Gossip                   │
│  - SWIM probes every 1s       │    │  - Receive probes             │
│  - Member state tracking      │    │  - Propagate member updates   │
└───────────────────────────────┘    └───────────────────────────────┘
         │ NodeJoin event                     │ NodeJoin event
         ▼                                    ▼
┌───────────────────────────────┐    ┌───────────────────────────────┐
│ QUIC Connection (:6970)       │◄──►│ QUIC Connection (:6970)       │
│  ┌─────────────────────────┐  │    │  ┌─────────────────────────┐  │
│  │ Stream: SYSTEM-MYCEL    │  │    │  │ Stream: SYSTEM-MYCEL    │  │
│  │ (Cache sync messages)   │  │    │  │ (Cache sync messages)   │  │
│  ├─────────────────────────┤  │    │  ├─────────────────────────┤  │
│  │ Stream: Custom App 1    │  │    │  │ Stream: Custom App 1    │  │
│  ├─────────────────────────┤  │    │  ├─────────────────────────┤  │
│  │ Stream: Custom App 2    │  │    │  │ Stream: Custom App 2    │  │
│  └─────────────────────────┘  │    │  └─────────────────────────┘  │
│  TLS 1.3 Encrypted           │    │  TLS 1.3 Encrypted           │
└───────────────────────────────┘    └───────────────────────────────┘
```

---

## Gossip Protocol Integration

### HashiCorp Memberlist Overview

**Memberlist** implements SWIM (Scalable Weakly-consistent Infection-style Process Group Membership) protocol with HashiCorp's "Lifeguard" extensions.

**Repository**: `github.com/hashicorp/memberlist`
**Status**: Production-ready, actively maintained
**Used by**: Consul, Nomad, Grafana Mimir (1,699+ dependent packages)

**What It Provides**:
- SWIM failure detection via direct + indirect probes
- Membership propagation via gossip (eventual consistency)
- Node states: Alive → Suspect → Dead
- Configurable probe intervals and timeouts
- AES-GCM encryption for gossip messages
- 512 bytes metadata per node
- Event callbacks (join/leave/update)

**What Nodosum Gains**:
- ✅ Replaces ~900 lines of custom code
- ✅ Production-grade failure detection
- ✅ Split-brain awareness
- ✅ Built-in Prometheus metrics
- ✅ Extensive documentation and community support

### Integration Pattern

**Memberlist Configuration**:
```go
config := memberlist.DefaultLANConfig()
config.Name = nodeID
config.BindPort = 6969
config.SecretKey = []byte(sharedSecret)
config.Events = &NodesumDelegate{nodosum: n}

ml, _ := memberlist.Create(config)
ml.Join(seedNodes)
```

**Event Delegate** (connects Memberlist to QUIC):
```go
type NodesumDelegate struct { nodosum *Nodosum }

func (d *NodesumDelegate) NotifyJoin(node *memberlist.Node) {
    addr := fmt.Sprintf("%s:%d", node.Addr, 6970)
    conn, _ := quic.Dial(addr, tlsConfig, quicConfig)
    d.nodosum.registerQuicConnection(node.Name, conn)
}

func (d *NodesumDelegate) NotifyLeave(node *memberlist.Node) {
    d.nodosum.closeQuicConnection(node.Name)
}
```

**Result**: Gossip manages membership, triggers QUIC connection lifecycle.

---

## QUIC Transport Layer

### Hybrid Protocol Stack

```
┌──────────────────────────────────────────┐
│      Application Interface (unchanged)    │
│   Send(payload, nodes) / SetReceiveFunc  │
└────────────────┬─────────────────────────┘
                 │
┌────────────────▼─────────────────────────┐
│        Application Stream Manager        │
│   Maps ApplicationID → quic.Stream       │
│   One long-lived stream per application  │
└────────────────┬─────────────────────────┘
                 │
┌────────────────▼─────────────────────────┐
│         QUIC Connection Manager          │
│   Maps NodeID → quic.Connection          │
│   Handles stream creation/teardown       │
└────────────────┬─────────────────────────┘
                 │
┌────────────────▼─────────────────────────┐
│          quic-go Library                 │
│   - TLS 1.3 handshake & encryption       │
│   - Stream multiplexing & flow control   │
│   - Congestion control & loss recovery   │
│   - Connection migration & 0-RTT         │
└────────────────┬─────────────────────────┘
                 │
┌────────────────▼─────────────────────────┐
│              UDP Socket                  │
└──────────────────────────────────────────┘
```

### Key Design Decisions

#### 1. Stream Strategy: Long-lived Streams per Application

**Chosen approach**: One bidirectional stream per application per connection

**Alternatives considered**:
- ❌ One stream per message: Too much overhead for small messages
- ❌ Single stream for all apps: Defeats multiplexing benefits
- ✅ One stream per application: Balance between overhead and isolation

**Implementation**:
```go
type nodeConn struct {
    connId       string
    conn         quic.Connection
    appStreams   sync.Map  // map[appID]quic.Stream
    ctx          context.Context
    cancel       context.CancelFunc
    streamMutex  sync.Mutex  // For stream creation
}

func (nc *nodeConn) getOrCreateStream(appID string) (quic.Stream, error) {
    // Check if stream exists
    if s, ok := nc.appStreams.Load(appID); ok {
        return s.(quic.Stream), nil
    }

    // Create new stream
    nc.streamMutex.Lock()
    defer nc.streamMutex.Unlock()

    stream, err := nc.conn.OpenStreamSync(nc.ctx)
    if err != nil {
        return nil, err
    }

    // Send stream initialization frame with appID
    writeStreamInit(stream, appID)

    nc.appStreams.Store(appID, stream)
    return stream, nil
}
```

#### 2. Handshake Strategy: QUIC + Custom Application Layer

**QUIC handles**:
- TLS 1.3 mutual authentication
- Connection establishment
- 0-RTT resumption

**Custom layer handles**:
- Node discovery (DNS-SD, Consul, static - unchanged)
- Node ID exchange (via stream init frame)
- Application registration synchronization

**Eliminated**:
- UDP HELLO/HELLO_ACK/HELLO_CONN packets
- ConnInit tie-breaking (QUIC allows simultaneous connections)
- One-time token exchange
- Plaintext shared secret

#### 3. Framing: Simplified Protocol

**Option A**: Keep existing Glutamate framing
- Pro: Minimal application-layer changes
- Con: Redundant with QUIC's own framing

**Option B**: Stream-native messaging (CHOSEN)
- Pro: Simpler, leverage QUIC's built-in features
- Con: Requires changing frame encode/decode

**New framing** (per stream message):
```
[4 bytes] Message Length (uint32, little-endian)
[N bytes] Payload (raw application data)
```

**Stream initialization frame** (sent once per stream):
```
[1 byte]  Frame type (STREAM_INIT = 0x01)
[2 bytes] ApplicationID length
[N bytes] ApplicationID (UTF-8 string)
```

#### 4. Connection Management: Accept Simultaneous Connections

**Current behavior**: ConnInit tie-breaking ensures only one node dials
**QUIC behavior**: Both nodes can dial simultaneously, QUIC handles it gracefully

**Implementation**: Accept first successful connection, close duplicate

```go
func (n *Nodosum) registerConnection(nodeID string, conn quic.Connection) {
    n.connMutex.Lock()
    defer n.connMutex.Unlock()

    if existing, ok := n.connections.Load(nodeID); ok {
        // Already have connection, close the new one
        conn.CloseWithError(0, "duplicate connection")
        return
    }

    // Store and initialize
    nc := &nodeConn{connId: nodeID, conn: conn, ...}
    n.connections.Store(nodeID, nc)
    go n.acceptStreams(nc)
}
```

#### 5. TLS Configuration: Mutual TLS

**Certificate requirements**:
- Each node needs valid TLS certificate
- CA pool for validating peer certificates
- Support for self-signed certs in development

**Configuration**:
```go
tlsConfig := &tls.Config{
    Certificates: []tls.Certificate{cfg.TlsCert},
    ClientCAs:    cfg.TlsCACert,
    ClientAuth:   tls.RequireAndVerifyClientCert,
    NextProtos:   []string{"mycorrizal-quic"},  // ALPN
}

quicConfig := &quic.Config{
    MaxIdleTimeout:        30 * time.Second,
    KeepAlivePeriod:       10 * time.Second,
    MaxIncomingStreams:    1000,  // Limit streams per connection
    MaxIncomingUniStreams: 0,     // Disable unidirectional streams
    EnableDatagrams:       false, // Don't need datagrams
}

listener, err := quic.Listen(conn, tlsConfig, quicConfig)
```

#### 6. Discovery Integration: Unchanged

**Current discovery mechanisms continue to work**:
- DNS-SD: Query service records, get node addresses
- Consul: Poll Consul API for service instances
- Static: Configured address list

**Change**: Discovery returns addresses, QUIC dials directly (no UDP handshake)

```go
// Discovery returns node addresses
nodes := n.discover()  // []DiscoveredNode

for _, node := range nodes {
    addr := fmt.Sprintf("%s:%s", node.Addr, node.Port)

    // Dial QUIC directly
    conn, err := quic.Dial(n.udpConn, addr, n.tlsConfig, n.quicConfig)
    if err != nil {
        continue
    }

    n.registerConnection(node.ID, conn)
}
```

---

## Migration Strategy

### Overview: 6-Week Migration Plan

**Phase 1-2: Memberlist Integration** (Weeks 1-2)
- Replace custom UDP handshake with Memberlist
- Implement event delegates
- Test membership management

**Phase 3-4: QUIC Transport** (Weeks 3-4)
- Add QUIC for application data
- Integrate with Memberlist events
- Update multiplexer

**Phase 5: Integration Testing** (Week 5)
- Multi-node cluster tests
- Failure scenarios
- Performance validation

**Phase 6: Production Hardening** (Week 6+)
- Metrics and observability
- Documentation
- Deployment preparation

---

### Phase 1: Memberlist Integration (Week 1)

**Goal**: Replace custom UDP handshake and node registry with HashiCorp Memberlist

#### Tasks

1. **Add memberlist dependency**
   ```bash
   go get github.com/hashicorp/memberlist
   ```

2. **Update Config struct** (`config.go`)
   - Remove: `TlsConfig` (QUIC handles TLS natively)
   - Add: `QuicConfig *quic.Config`
   - Keep: `TlsCert`, `TlsCACert` (used by QUIC)
   - Add: `ALPNProtocol string` (default: "mycorrizal-quic")

3. **Replace listeners** (`nodosum.go`)
   - Remove: `listenTcp()`, `listenUdp()`
   - Add: `listenQuic()` - creates `quic.Listener`
   - Update: `Start()` method to call `listenQuic()`

4. **Update nodeConn struct** (`conn.go`)
   ```go
   type nodeConn struct {
       connId      string
       conn        quic.Connection  // was: net.Conn
       appStreams  sync.Map         // new: map[appID]quic.Stream
       ctx         context.Context
       cancel      context.CancelFunc
       streamMutex sync.Mutex       // new: for stream creation
   }
   ```

5. **Implement basic QUIC accept loop**
   ```go
   func (n *Nodosum) listenQuic() error {
       listener, err := quic.Listen(n.udpConn, n.tlsConfig, n.quicConfig)
       if err != nil {
           return err
       }

       for {
           conn, err := listener.Accept(n.ctx)
           if err != nil {
               return err
           }

           go n.handleQuicConnection(conn)
       }
   }
   ```

6. **Basic connection handling**
   - Accept connection
   - Extract peer ID from certificate CN or SAN
   - Register connection
   - Don't implement streams yet (just verify connections work)

**Deliverable**: Nodes can establish QUIC connections with mTLS

**Testing**: Manual test with 2 nodes, verify connection in logs

---

### Phase 2: Stream Management (Week 2)

**Goal**: Implement stream creation, routing, and lifecycle management

#### Tasks

1. **Implement stream acceptance** (`conn.go`)
   ```go
   func (n *Nodosum) acceptStreams(nc *nodeConn) {
       defer n.wg.Done()

       for {
           stream, err := nc.conn.AcceptStream(nc.ctx)
           if err != nil {
               return  // Connection closed
           }

           n.wg.Add(1)
           go n.handleStream(nc, stream)
       }
   }
   ```

2. **Stream initialization protocol**
   - Define stream init frame format
   - Client sends appID on stream open
   - Server reads appID, registers stream
   - Store stream in `nodeConn.appStreams`

3. **Implement stream read loop**
   ```go
   func (n *Nodosum) handleStream(nc *nodeConn, stream quic.Stream) {
       defer n.wg.Done()
       defer stream.Close()

       // Read stream init frame
       appID, err := readStreamInit(stream)
       if err != nil {
           return
       }

       // Register stream
       nc.appStreams.Store(appID, stream)

       // Read messages in loop
       for {
           payload, err := readMessage(stream)
           if err != nil {
               return
           }

           // Route to application (via existing multiplexer)
           n.globalReadChannel <- &frame{
               ApplicationID: appID,
               Payload:      payload,
           }
       }
   }
   ```

4. **Implement stream write mechanism**
   ```go
   func (nc *nodeConn) sendToStream(appID string, payload []byte) error {
       stream, err := nc.getOrCreateStream(appID)
       if err != nil {
           return err
       }

       return writeMessage(stream, payload)
   }
   ```

5. **Update protocol.go**
   - Add `readStreamInit()`, `writeStreamInit()`
   - Add `readMessage()`, `writeMessage()`
   - Simplify frame format (remove complex Glutamate header)
   - Keep backward compatibility helpers for testing

**Deliverable**: Applications can send/receive messages over QUIC streams

**Testing**:
- Unit tests for stream init protocol
- Integration test with 2 nodes, 2 applications each

---

### Phase 3: Multiplexer Integration (Week 3)

**Goal**: Update multiplexer to work with QUIC streams, maintain application interface

#### Tasks

1. **Update multiplexer outbound** (`mux.go`)
   ```go
   func (n *Nodosum) multiplexerTaskOutbound(input interface{}) (any, error) {
       dp := input.(dataPackage)

       for _, nodeID := range dp.receivingNodes {
           nc, ok := n.connections.Load(nodeID)
           if !ok {
               continue
           }

           // Send directly to stream (not to write channel)
           err := nc.(*nodeConn).sendToStream(dp.applicationID, dp.data)
           if err != nil {
               n.logger.Error("stream write failed", "error", err)
           }
       }

       return nil, nil
   }
   ```

2. **Remove obsolete components**
   - Delete: `readLoop()`, `writeLoop()` in `conn.go`
   - Delete: Per-connection write channels
   - Keep: `globalReadChannel` for inbound routing
   - Keep: `globalWriteChannel` for outbound requests

3. **Update connection registry** (`registry.go`)
   - Simplify `closeConnection()` - just close QUIC connection
   - Remove token management
   - Keep metadata updates

4. **Handle stream errors**
   - Stream closure → log error, remove from map, app continues
   - Connection closure → close all streams, mark node offline
   - Reconnection logic remains unchanged

5. **Update application workers** (`application.go`)
   - Keep send worker (pushes to globalWriteChannel)
   - Keep receive worker (receives from app input channel)
   - No changes needed to Application interface

**Deliverable**: Full multiplexer functionality with QUIC

**Testing**:
- 3+ nodes with multiple applications
- Verify message routing
- Test stream failure scenarios

---

### Phase 4: Discovery & Handshake (Week 3-4)

**Goal**: Update discovery to dial QUIC directly, remove UDP handshake

#### Tasks

1. **Update discovery implementations**
   - `discoverDns.go`: No changes needed (returns addresses)
   - `discoverConsul.go`: No changes needed
   - Static discovery: No changes needed

2. **Implement QUIC dialing** (`nodosum.go`)
   ```go
   func (n *Nodosum) dialNode(node DiscoveredNode) error {
       addr := fmt.Sprintf("%s:%s", node.Addr, node.Port)

       conn, err := quic.Dial(
           n.udpConn,
           addr,
           n.tlsConfig,
           n.quicConfig,
       )
       if err != nil {
           return err
       }

       n.registerConnection(node.ID, conn)
       go n.acceptStreams(...)

       return nil
   }
   ```

3. **Remove UDP handshake code**
   - Delete: `handleUdpPacket()` in `nodosum.go`
   - Delete: UDP packet types (HELLO, HELLO_ACK, etc.) in `protocol.go`
   - Delete: `connInit` struct and tie-breaking logic
   - Delete: Token generation and validation

4. **Handle duplicate connections**
   - Accept first successful connection
   - Close duplicate attempts gracefully
   - Log for debugging

5. **Implement periodic discovery**
   - Keep existing 30-second discovery loop
   - Dial newly discovered nodes
   - Skip already-connected nodes

**Deliverable**: Full discovery-to-connection flow with QUIC

**Testing**:
- DNS-SD discovery with 5+ nodes
- Node restart scenarios (0-RTT validation)
- Simultaneous connection attempts

---

### Phase 5: Testing & Optimization (Week 4)

**Goal**: Comprehensive testing, performance tuning, documentation

#### Tasks

1. **Unit tests**
   - Stream initialization protocol
   - Message framing (read/write)
   - Connection management
   - Error handling

2. **Integration tests**
   - Multi-node cluster formation
   - Application message routing
   - Node failure and recovery
   - Connection migration simulation

3. **Performance benchmarks** (see Benchmarking section)
   - Latency comparison (TCP vs QUIC)
   - Throughput with multiple applications
   - Connection setup time
   - Memory usage

4. **Documentation**
   - Update `CLAUDE.md` with QUIC architecture
   - Update `README.md` if applicable
   - Add QUIC configuration examples
   - Document TLS certificate requirements

5. **Code cleanup**
   - Remove dead code (UDP handshake, old framing)
   - Simplify configuration
   - Add detailed comments to new code
   - Run `go vet`, `golangci-lint`

**Deliverable**: Production-ready QUIC implementation

---

## Detailed Code Changes

### File-by-File Breakdown

#### 1. `config.go` (10-15 changes)

**Add**:
```go
QuicConfig *quic.Config  // QUIC transport configuration
ALPNProtocol string      // Application-Layer Protocol Negotiation
```

**Remove**:
```go
// TlsConfig is replaced by QuicConfig + embedded TLS
```

**Modify**:
```go
func GetDefaultConfig() *Config {
    return &Config{
        QuicConfig: &quic.Config{
            MaxIdleTimeout:     30 * time.Second,
            KeepAlivePeriod:    10 * time.Second,
            MaxIncomingStreams: 1000,
        },
        ALPNProtocol: "mycorrizal-quic",
        // ... existing fields
    }
}
```

**Validation**:
```go
func (c *Config) Validate() error {
    // Add QUIC-specific validations
    if c.QuicConfig == nil {
        return errors.New("QuicConfig required")
    }
    if c.TlsCert == nil {
        return errors.New("TLS certificate required for QUIC")
    }
    // ... existing validations
}
```

---

#### 2. `nodosum.go` (30-40 changes)

**Struct updates**:
```go
type Nodosum struct {
    // Remove:
    // - tcpListener net.Listener
    // - udpConn *net.UDPConn

    // Add:
    quicListener quic.Listener
    udpConn      net.UDPConn  // Keep for quic.Listen

    // Unchanged:
    id, sharedSecret, ctx, cancel, wg, config, logger, ...
    connections, applications, globalReadChannel, globalWriteChannel
}
```

**Replace listener methods**:
```go
// DELETE: func (n *Nodosum) listenTcp() error
// DELETE: func (n *Nodosum) listenUdp() error
// DELETE: func (n *Nodosum) handleUdpPacket(hp handshakePacket)

// ADD:
func (n *Nodosum) listenQuic() error {
    addr := fmt.Sprintf(":%s", n.config.ConnectPort)
    udpAddr, _ := net.ResolveUDPAddr("udp", addr)

    udpConn, err := net.ListenUDP("udp", udpAddr)
    if err != nil {
        return err
    }
    n.udpConn = udpConn

    tlsConfig := &tls.Config{
        Certificates: []tls.Certificate{*n.config.TlsCert},
        ClientCAs:    n.config.TlsCACert,
        ClientAuth:   tls.RequireAndVerifyClientCert,
        NextProtos:   []string{n.config.ALPNProtocol},
    }

    listener, err := quic.Listen(udpConn, tlsConfig, n.config.QuicConfig)
    if err != nil {
        return err
    }
    n.quicListener = listener

    go n.acceptConnections()
    return nil
}

func (n *Nodosum) acceptConnections() {
    defer n.wg.Done()

    for {
        conn, err := n.quicListener.Accept(n.ctx)
        if err != nil {
            if n.ctx.Err() != nil {
                return  // Shutting down
            }
            n.logger.Error("accept failed", "error", err)
            continue
        }

        n.wg.Add(1)
        go n.handleQuicConnection(conn)
    }
}

func (n *Nodosum) handleQuicConnection(conn quic.Connection) {
    defer n.wg.Done()

    // Extract peer ID from certificate
    peerCert := conn.ConnectionState().TLS.PeerCertificates[0]
    peerID := peerCert.Subject.CommonName  // Or from SAN

    // Register connection
    if !n.registerConnection(peerID, conn) {
        conn.CloseWithError(0, "duplicate connection")
        return
    }

    // Accept streams
    n.wg.Add(1)
    go n.acceptStreams(peerID)
}
```

**Update Start() method**:
```go
func (n *Nodosum) Start() error {
    // Replace:
    // go n.listenTcp()
    // go n.listenUdp()

    // With:
    if err := n.listenQuic(); err != nil {
        return err
    }

    // Start multiplexer (unchanged)
    // ...

    // Discover and dial peers
    go n.discoveryLoop()

    close(n.ready)
    return nil
}
```

**Add discovery dialing**:
```go
func (n *Nodosum) discoveryLoop() {
    defer n.wg.Done()

    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-n.ctx.Done():
            return
        case <-ticker.C:
            nodes := n.discover()
            for _, node := range nodes {
                if node.ID == n.id {
                    continue  // Skip self
                }

                if _, ok := n.connections.Load(node.ID); ok {
                    continue  // Already connected
                }

                if err := n.dialNode(node); err != nil {
                    n.logger.Error("dial failed", "node", node.ID, "error", err)
                }
            }
        }
    }
}

func (n *Nodosum) dialNode(node DiscoveredNode) error {
    addr := fmt.Sprintf("%s:%s", node.Addr, node.ConnectPort)

    tlsConfig := &tls.Config{
        ServerName:   node.Addr,  // For SNI
        RootCAs:      n.config.TlsCACert,
        Certificates: []tls.Certificate{*n.config.TlsCert},
        NextProtos:   []string{n.config.ALPNProtocol},
    }

    conn, err := quic.Dial(n.udpConn, addr, tlsConfig, n.config.QuicConfig)
    if err != nil {
        return err
    }

    // Extract peer ID from certificate
    peerCert := conn.ConnectionState().TLS.PeerCertificates[0]
    peerID := peerCert.Subject.CommonName

    if !n.registerConnection(peerID, conn) {
        conn.CloseWithError(0, "duplicate connection")
        return fmt.Errorf("duplicate connection to %s", peerID)
    }

    n.wg.Add(1)
    go n.acceptStreams(peerID)

    return nil
}
```

**Update Shutdown**:
```go
func (n *Nodosum) Shutdown() error {
    n.cancel()

    // Close QUIC listener
    if n.quicListener != nil {
        n.quicListener.Close()
    }

    // Close all connections
    n.connections.Range(func(key, value interface{}) bool {
        nc := value.(*nodeConn)
        nc.conn.CloseWithError(0, "shutting down")
        return true
    })

    n.wg.Wait()
    return nil
}
```

---

#### 3. `conn.go` (50-60 changes - largest refactor)

**Update nodeConn struct**:
```go
type nodeConn struct {
    connId      string
    conn        quic.Connection  // Changed from net.Conn
    appStreams  sync.Map         // map[string]quic.Stream
    streamMutex sync.Mutex
    ctx         context.Context
    cancel      context.CancelFunc

    // Remove:
    // readChan  chan any
    // writeChan chan any
}
```

**DELETE entire sections**:
- `readLoop()` function
- `writeLoop()` function
- `serverHandshake()` function (QUIC handles handshake)
- UDP-related functions

**ADD stream management**:
```go
func (n *Nodosum) acceptStreams(nodeID string) {
    defer n.wg.Done()

    nc, ok := n.connections.Load(nodeID)
    if !ok {
        return
    }
    conn := nc.(*nodeConn)

    for {
        stream, err := conn.conn.AcceptStream(conn.ctx)
        if err != nil {
            n.logger.Debug("stream accept ended", "node", nodeID, "error", err)
            return
        }

        n.wg.Add(1)
        go n.handleStream(nodeID, stream)
    }
}

func (n *Nodosum) handleStream(nodeID string, stream quic.Stream) {
    defer n.wg.Done()
    defer stream.Close()

    // Read stream init frame
    frameType, err := readByte(stream)
    if err != nil {
        return
    }

    if frameType != FRAME_STREAM_INIT {
        n.logger.Error("invalid stream init", "type", frameType)
        return
    }

    appID, err := readString(stream)
    if err != nil {
        return
    }

    // Register stream
    nc, _ := n.connections.Load(nodeID)
    nc.(*nodeConn).appStreams.Store(appID, stream)

    // Read messages in loop
    for {
        payload, err := readMessage(stream)
        if err != nil {
            if err != io.EOF {
                n.logger.Error("stream read failed", "error", err)
            }
            return
        }

        // Route to application via existing multiplexer
        n.globalReadChannel <- &frame{
            ApplicationID: appID,
            Payload:      payload,
        }
    }
}

func (nc *nodeConn) getOrCreateStream(appID string) (quic.Stream, error) {
    // Check existing
    if s, ok := nc.appStreams.Load(appID); ok {
        return s.(quic.Stream), nil
    }

    // Create new
    nc.streamMutex.Lock()
    defer nc.streamMutex.Unlock()

    // Double-check after lock
    if s, ok := nc.appStreams.Load(appID); ok {
        return s.(quic.Stream), nil
    }

    stream, err := nc.conn.OpenStreamSync(nc.ctx)
    if err != nil {
        return nil, err
    }

    // Send stream init
    if err := writeStreamInit(stream, appID); err != nil {
        stream.Close()
        return nil, err
    }

    nc.appStreams.Store(appID, stream)
    return stream, nil
}

func (nc *nodeConn) sendToStream(appID string, payload []byte) error {
    stream, err := nc.getOrCreateStream(appID)
    if err != nil {
        return err
    }

    return writeMessage(stream, payload)
}
```

---

#### 4. `protocol.go` (30-40 changes)

**Keep frame struct** (used internally):
```go
type frame struct {
    Version       byte
    Type          byte
    Flag          byte
    Length        uint32
    ApplicationID string
    Payload       []byte
}
```

**DELETE**:
- All UDP packet types (`handshakePacket`, `PACKET_TYPE_HELLO`, etc.)
- UDP encoding/decoding functions
- Complex Glutamate frame encoding (replaced with simpler version)

**SIMPLIFY frame encoding** (only for internal use now):
```go
// Stream messages use simpler format
func readMessage(stream quic.Stream) ([]byte, error) {
    // Read length (4 bytes)
    var length uint32
    if err := binary.Read(stream, binary.LittleEndian, &length); err != nil {
        return nil, err
    }

    // Read payload
    payload := make([]byte, length)
    if _, err := io.ReadFull(stream, payload); err != nil {
        return nil, err
    }

    return payload, nil
}

func writeMessage(stream quic.Stream, payload []byte) error {
    // Write length
    length := uint32(len(payload))
    if err := binary.Write(stream, binary.LittleEndian, length); err != nil {
        return err
    }

    // Write payload
    _, err := stream.Write(payload)
    return err
}
```

**ADD stream init protocol**:
```go
const (
    FRAME_STREAM_INIT = 0x01
)

func writeStreamInit(stream quic.Stream, appID string) error {
    // Frame type
    if _, err := stream.Write([]byte{FRAME_STREAM_INIT}); err != nil {
        return err
    }

    // AppID length (2 bytes)
    appIDBytes := []byte(appID)
    length := uint16(len(appIDBytes))
    if err := binary.Write(stream, binary.LittleEndian, length); err != nil {
        return err
    }

    // AppID
    _, err := stream.Write(appIDBytes)
    return err
}

func readByte(stream quic.Stream) (byte, error) {
    buf := make([]byte, 1)
    if _, err := io.ReadFull(stream, buf); err != nil {
        return 0, err
    }
    return buf[0], nil
}

func readString(stream quic.Stream) (string, error) {
    // Read length
    var length uint16
    if err := binary.Read(stream, binary.LittleEndian, &length); err != nil {
        return "", err
    }

    // Read string
    buf := make([]byte, length)
    if _, err := io.ReadFull(stream, buf); err != nil {
        return "", err
    }

    return string(buf), nil
}
```

---

#### 5. `mux.go` (10-15 changes)

**Update outbound multiplexer**:
```go
func (n *Nodosum) multiplexerTaskOutbound(input interface{}) (any, error) {
    dp, ok := input.(dataPackage)
    if !ok {
        return nil, fmt.Errorf("invalid input type")
    }

    // Determine target nodes
    nodes := dp.receivingNodes
    if len(nodes) == 0 {
        // Broadcast: get all alive nodes
        nodes = n.getAliveNodes()
    }

    // Send to each node's stream
    for _, nodeID := range nodes {
        nc, ok := n.connections.Load(nodeID)
        if !ok {
            continue
        }

        conn := nc.(*nodeConn)
        if err := conn.sendToStream(dp.applicationID, dp.data); err != nil {
            n.logger.Error("stream write failed",
                "node", nodeID,
                "app", dp.applicationID,
                "error", err)
        }
    }

    return nil, nil
}
```

**Inbound multiplexer**: No changes needed (still receives from `globalReadChannel`)

**Remove**: Per-connection write channel management

---

#### 6. `registry.go` (5-10 changes)

**Simplify connection registration**:
```go
func (n *Nodosum) registerConnection(nodeID string, conn quic.Connection) bool {
    // Check for duplicate
    if _, exists := n.connections.Load(nodeID); exists {
        return false
    }

    ctx, cancel := context.WithCancel(n.ctx)

    nc := &nodeConn{
        connId:      nodeID,
        conn:        conn,
        appStreams:  sync.Map{},
        streamMutex: sync.Mutex{},
        ctx:         ctx,
        cancel:      cancel,
    }

    n.connections.Store(nodeID, nc)

    // Update metadata
    n.NodeMetaMapMutex.Lock()
    if meta, ok := n.NodeMetaMap[nodeID]; ok {
        meta.alive = true
    }
    n.NodeMetaMapMutex.Unlock()

    n.logger.Info("node connected", "id", nodeID)
    return true
}
```

**Simplify closeConnection**:
```go
func (n *Nodosum) closeConnection(nodeID string) {
    nc, ok := n.connections.Load(nodeID)
    if !ok {
        return
    }

    conn := nc.(*nodeConn)
    conn.cancel()
    conn.conn.CloseWithError(0, "closing connection")

    n.connections.Delete(nodeID)

    // Update metadata
    n.NodeMetaMapMutex.Lock()
    if meta, ok := n.NodeMetaMap[nodeID]; ok {
        meta.alive = false
    }
    n.NodeMetaMapMutex.Unlock()
}
```

**DELETE**:
- Token management functions
- Write channel creation

---

#### 7. `application.go` (0-5 changes)

**Minimal changes** - application interface remains the same!

**Possible optimization**: Remove send worker, call multiplexer directly
```go
func (a *application) Send(payload []byte, ids []string) error {
    dp := dataPackage{
        applicationID:  a.uniqueIdentifier,
        data:          payload,
        receivingNodes: ids,
    }

    // Option A: Keep worker (current)
    a.sendWorker.InputChan <- dp

    // Option B: Direct call (optimization)
    // return a.nodosum.multiplexerTaskOutbound(dp)

    return nil
}
```

**No changes needed**: `SetReceiveFunc()`, `Nodes()`, application workers

---

#### 8. `acl.go` (potential deletion or minimal changes)

**Current state**: Placeholder implementation, not actively used

**Options**:
1. **Delete entirely**: QUIC mTLS handles authentication
2. **Repurpose**: Use for authorization (not authentication)
3. **Keep for future**: Application-level ACLs

**Recommendation**: Keep but document as "future use" for now

---

#### 9. Discovery files (0 changes)

**No changes needed** to:
- `discoverDns.go`
- `discoverConsul.go`
- `discoverStatic.go`

These return node addresses, which are used by QUIC dialing (same as before)

---

### New Files to Create

#### `quic_helpers.go` (optional)

Centralize QUIC-specific utilities:
```go
package nodosum

import (
    "crypto/tls"
    "github.com/quic-go/quic-go"
)

func createQuicTLSConfig(cfg *Config) *tls.Config {
    return &tls.Config{
        Certificates: []tls.Certificate{*cfg.TlsCert},
        ClientCAs:    cfg.TlsCACert,
        ClientAuth:   tls.RequireAndVerifyClientCert,
        NextProtos:   []string{cfg.ALPNProtocol},
    }
}

func extractPeerID(conn quic.Connection) string {
    certs := conn.ConnectionState().TLS.PeerCertificates
    if len(certs) == 0 {
        return ""
    }

    // Try CommonName
    if cn := certs[0].Subject.CommonName; cn != "" {
        return cn
    }

    // Try SAN
    if len(certs[0].DNSNames) > 0 {
        return certs[0].DNSNames[0]
    }

    return ""
}
```

---

## Testing & Validation

### Unit Tests

#### Test Categories

1. **Stream Protocol Tests** (`protocol_test.go`)
   ```go
   func TestStreamInit(t *testing.T)
   func TestMessageReadWrite(t *testing.T)
   func TestInvalidFrameHandling(t *testing.T)
   ```

2. **Connection Management Tests** (`conn_test.go`)
   ```go
   func TestStreamCreation(t *testing.T)
   func TestDuplicateStreamPrevention(t *testing.T)
   func TestStreamCleanupOnError(t *testing.T)
   func TestConnectionCleanup(t *testing.T)
   ```

3. **Multiplexer Tests** (`mux_test.go`)
   ```go
   func TestOutboundRouting(t *testing.T)
   func TestInboundRouting(t *testing.T)
   func TestBroadcast(t *testing.T)
   ```

4. **Application Interface Tests** (`application_test.go`)
   ```go
   func TestApplicationSend(t *testing.T)
   func TestApplicationReceive(t *testing.T)
   func TestMultipleApplications(t *testing.T)
   ```

### Integration Tests

#### Test Scenarios

1. **Basic Cluster Formation**
   - Start 3 nodes with static discovery
   - Verify all nodes connect to each other
   - Verify connection count (3 nodes = 3 bidirectional connections)

2. **Application Messaging**
   - Register same app on all nodes
   - Send message from node A
   - Verify received on nodes B and C
   - Verify payload integrity

3. **Multiple Applications**
   - 3 nodes with 3 different applications each
   - Send 100 messages concurrently
   - Verify all delivered correctly
   - Verify no cross-contamination

4. **Node Failure Recovery**
   - 3-node cluster running
   - Kill node B
   - Verify nodes A and C detect failure
   - Restart node B
   - Verify reconnection (0-RTT)
   - Verify message delivery resumes

5. **Stream Isolation**
   - 2 nodes with 2 apps each
   - Block reads on app1's stream
   - Verify app2 continues unaffected (no head-of-line blocking)

6. **Concurrent Connection Attempts**
   - Configure nodes A and B to dial each other simultaneously
   - Verify only one connection established
   - Verify both can send/receive

7. **TLS Mutual Authentication**
   - Node with invalid certificate attempts connection
   - Verify rejection
   - Verify logs show TLS error

#### Integration Test Framework

```go
// test_cluster.go
type TestCluster struct {
    nodes   []*Nodosum
    configs []*Config
    cleanup func()
}

func NewTestCluster(nodeCount int) *TestCluster {
    // Generate self-signed certs for each node
    // Create configs with static discovery pointing to each other
    // Start all nodes
    // Wait for cluster formation
}

func (tc *TestCluster) WaitForFullMesh(timeout time.Duration) error {
    // Poll until all nodes connected to all others
}

func (tc *TestCluster) Cleanup() {
    // Shutdown all nodes
}

// Usage:
func TestFullMesh(t *testing.T) {
    cluster := NewTestCluster(5)
    defer cluster.Cleanup()

    if err := cluster.WaitForFullMesh(10 * time.Second); err != nil {
        t.Fatal(err)
    }

    // Run test scenarios
}
```

### Load Tests

#### Scenarios

1. **Throughput Test**
   - 2 nodes, 1 application
   - Send 10,000 1KB messages as fast as possible
   - Measure messages/sec, latency distribution

2. **Concurrency Test**
   - 2 nodes, 10 applications
   - Each app sends 1,000 messages concurrently
   - Verify all delivered, measure total time

3. **Large Cluster Test**
   - 20 nodes (full mesh = 380 bidirectional connections)
   - 5 applications per node
   - Broadcast 100 messages from each node
   - Measure cluster formation time, message delivery rate

4. **Stream Churn Test**
   - 2 nodes, rapidly create/destroy applications
   - 100 applications created, each sends 10 messages, then unregisters
   - Verify no stream/memory leaks

#### Metrics to Collect

- **Latency**: p50, p95, p99 message delivery time
- **Throughput**: Messages/sec per stream, per connection, cluster-wide
- **Connection Time**: Time to establish QUIC connection
- **0-RTT Success Rate**: % of reconnections using 0-RTT
- **Memory Usage**: Per connection, per stream, total
- **CPU Usage**: Idle, under load
- **Stream Count**: Active streams per connection

---

## Performance Benchmarking

### Benchmark Plan

#### Baseline: Current TCP/UDP Implementation

**Measure before migration**:

1. **Connection Setup Latency**
   ```bash
   # Measure time from discovery to first message sent
   go test -bench=BenchmarkConnectionSetup -benchtime=100x
   ```
   Expected: ~50-100ms (UDP handshake + TCP handshake + TLS)

2. **Message Latency**
   ```bash
   go test -bench=BenchmarkMessageLatency -benchtime=10000x
   ```
   Expected: ~1-5ms for LAN, ~50-100ms for WAN

3. **Throughput**
   ```bash
   go test -bench=BenchmarkThroughput -benchtime=10s
   ```
   Expected: ~50,000 msgs/sec for small messages, ~500MB/sec for large

4. **Multi-Application Performance**
   ```bash
   go test -bench=BenchmarkMultiApp -benchtime=10s
   ```
   Expected: Linear scaling up to 10 apps, degradation after (channel contention)

#### Target: QUIC Implementation

**Same benchmarks, compare results**:

**Expected improvements**:
- Connection setup: 20-30% faster (single handshake)
- Message latency: Similar or 5-10% better
- Throughput (single app): Similar
- Throughput (multi-app): 30-50% better (no head-of-line blocking)
- Reconnection: 80-90% faster (0-RTT)

#### Benchmark Code Structure

```go
// benchmark_test.go

func BenchmarkConnectionSetup(b *testing.B) {
    // Setup: 2 nodes, discovery configured
    // Benchmark: Time from Start() to connection ready
    // Cleanup
}

func BenchmarkMessageLatency(b *testing.B) {
    // Setup: 2 connected nodes, 1 app
    // Benchmark: Time from Send() to Receive callback
    // Report: b.ReportMetric(latency, "ns/op")
}

func BenchmarkThroughput(b *testing.B) {
    // Setup: 2 connected nodes, 1 app
    // Benchmark: Send N messages, measure total time
    // Report: b.ReportMetric(msgPerSec, "msgs/sec")
}

func BenchmarkMultiApp(b *testing.B) {
    apps := []int{1, 5, 10, 20, 50}
    for _, appCount := range apps {
        b.Run(fmt.Sprintf("apps=%d", appCount), func(b *testing.B) {
            // Setup: appCount applications
            // Benchmark: Send N messages from each app
            // Report: aggregate throughput
        })
    }
}

func BenchmarkLargeCluster(b *testing.B) {
    // Setup: 20 nodes
    // Benchmark: Cluster formation time + message propagation
    // Report: b.ReportMetric(formationTime, "ms/cluster")
}
```

#### Comparison Report Template

```markdown
## Benchmark Results: TCP/UDP vs QUIC

### Test Environment
- CPU: [details]
- RAM: [details]
- Network: [LAN/WAN, latency, bandwidth]
- Go version: [version]

### Results

| Metric | TCP/UDP | QUIC | Change |
|--------|---------|------|--------|
| Connection setup (ms) | X | Y | ±Z% |
| Message latency p50 (μs) | X | Y | ±Z% |
| Message latency p99 (μs) | X | Y | ±Z% |
| Throughput 1 app (msgs/s) | X | Y | ±Z% |
| Throughput 10 apps (msgs/s) | X | Y | ±Z% |
| Reconnection time (ms) | X | Y | ±Z% |
| Memory per connection (KB) | X | Y | ±Z% |

### Observations
- [Key findings]
- [Performance improvements]
- [Any regressions and reasons]
```

---

## Rollout Plan

### Pre-Deployment Checklist

- [ ] All unit tests passing
- [ ] All integration tests passing
- [ ] Benchmarks show acceptable performance
- [ ] Code review completed
- [ ] Documentation updated
- [ ] Example configurations created
- [ ] Certificate generation instructions documented

### Deployment Phases

Given **no backward compatibility requirement**, rollout is straightforward:

#### Phase 1: Development Deployment

**Target**: Internal development environments

1. Generate test certificates for dev cluster
2. Deploy QUIC version to 3-node dev cluster
3. Run smoke tests (basic functionality)
4. Monitor for 24 hours
5. Review logs for unexpected errors

**Rollback**: Revert to TCP/UDP version if critical issues

#### Phase 2: Staging Deployment

**Target**: Staging environment mimicking production

1. Deploy to staging cluster (5-10 nodes)
2. Run full integration test suite
3. Run load tests with production-like traffic
4. Monitor metrics:
   - Connection success rate
   - Message delivery rate
   - Error logs
   - Resource usage
5. Soak test for 1 week

**Success criteria**:
- 99.9%+ connection success rate
- Zero message loss
- No memory leaks
- Resource usage within acceptable limits

#### Phase 3: Production Deployment

**Target**: Production environment

**Since no backward compatibility needed**: Can do rolling deployment

1. **Deploy to first production node**
   - Single node update
   - Monitor for 24 hours
   - Verify connectivity to other nodes

2. **Rolling deployment**
   - Update nodes in batches (20% at a time)
   - 1-hour soak between batches
   - Monitor connection churn

3. **Full cluster on QUIC**
   - Verify full mesh connectivity
   - Run production smoke tests
   - Monitor for 1 week

**Monitoring during rollout**:
- Connection establishment rate
- Connection failure rate
- Message throughput
- Latency percentiles
- Error rates
- Resource utilization

**Rollback plan**:
- Keep TCP/UDP version binaries available
- Scripted rollback process
- Database/state should be compatible (no changes)

### Post-Deployment

#### Week 1: Active Monitoring
- Daily review of metrics
- Investigation of any anomalies
- Performance tuning based on observations

#### Week 2-4: Optimization
- Analyze performance data
- Tune QUIC configuration parameters:
  - `MaxIdleTimeout`
  - `KeepAlivePeriod`
  - `MaxIncomingStreams`
  - Buffer sizes
- Implement any needed fixes

#### Month 2+: Stability
- Monitor for long-term issues (memory leaks, etc.)
- Collect user feedback
- Document learnings

---

## Risk Assessment

### High-Risk Areas

#### 1. Stream Lifecycle Management

**Risk**: Stream leaks or premature closures

**Mitigation**:
- Comprehensive error handling in stream read/write loops
- Proper cleanup in defer statements
- Metrics tracking active stream count
- Integration tests for stream lifecycle edge cases

**Detection**:
- Monitor stream count over time (should be stable)
- Memory profiling to detect leaks
- Alert on unexpected stream closures

#### 2. Connection Migration Edge Cases

**Risk**: Connection migration causing duplicate connections or lost state

**Mitigation**:
- Robust duplicate connection detection
- Connection state tracking in metadata
- Testing with network changes (IP change simulation)

**Detection**:
- Monitor connection churn metrics
- Log all connection migration events
- Alert on duplicate connection attempts

#### 3. TLS Certificate Issues

**Risk**: Certificate validation failures, expiry, misconfiguration

**Mitigation**:
- Thorough documentation of cert requirements
- Automated cert generation for development
- Cert expiry monitoring
- Detailed error messages for TLS failures

**Detection**:
- Alert on TLS handshake failures
- Monitor cert expiry dates
- Log all TLS errors with details

### Medium-Risk Areas

#### 4. Performance Regression

**Risk**: QUIC implementation slower than TCP for some workloads

**Mitigation**:
- Comprehensive benchmarking before deployment
- Load testing with realistic traffic
- Tunable QUIC configuration
- Performance profiling

**Detection**:
- Continuous performance monitoring
- Comparison to baseline metrics
- p99 latency alerts

#### 5. Multiplexer Integration

**Risk**: Incorrect frame routing, message loss

**Mitigation**:
- Extensive unit tests for routing logic
- Integration tests with multiple applications
- Message delivery tracking in tests

**Detection**:
- Application-level acknowledgments
- Message counters and validation
- Alert on unexpected message drops

#### 6. Dependency Risk (quic-go library)

**Risk**: Bugs in quic-go, breaking changes, abandonment

**Mitigation**:
- Use stable, well-tested version
- Pin dependency version
- Monitor quic-go repository for issues
- Fallback plan to fork if needed

**Detection**:
- Monitor quic-go issue tracker
- Review changelogs before upgrades
- Test thoroughly after library updates

### Low-Risk Areas

#### 7. Discovery Integration

**Risk**: Discovery mechanisms incompatible with QUIC

**Mitigation**:
- Discovery layer unchanged (returns addresses)
- Testing with all discovery modes (DNS-SD, Consul, static)

**Detection**:
- Monitor discovery success rate
- Alert on repeated discovery failures

#### 8. Application Interface Changes

**Risk**: Breaking changes to Application API affecting higher layers

**Mitigation**:
- Maintain identical API contract
- Integration tests for Application interface
- Test with Mycel layer (existing code)

**Detection**:
- Compile-time errors (would catch immediately)
- Integration test failures

---

## References

### Gossip Protocol & Memberlist

- **HashiCorp Memberlist**: SWIM gossip protocol implementation
  https://github.com/hashicorp/memberlist

- **Memberlist Documentation**
  https://pkg.go.dev/github.com/hashicorp/memberlist

- **SWIM Paper**: "SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol" (Cornell, 2002)
  https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf

- **Lifeguard Extensions**: HashiCorp's improvements to SWIM
  https://arxiv.org/pdf/1707.00788.pdf

- **Consul Architecture**: How Consul uses Memberlist
  https://www.consul.io/docs/architecture

### QUIC Protocol

- **RFC 9000**: QUIC Transport Protocol
  https://www.rfc-editor.org/rfc/rfc9000.html

- **RFC 9001**: Using TLS to Secure QUIC
  https://www.rfc-editor.org/rfc/rfc9001.html

- **RFC 9002**: QUIC Loss Detection and Congestion Control
  https://www.rfc-editor.org/rfc/rfc9002.html

- **RFC 9221**: QUIC Datagrams Extension
  https://www.rfc-editor.org/rfc/rfc9221.html

### Libraries and Tools

- **quic-go**: QUIC implementation in Go
  https://github.com/quic-go/quic-go

- **quic-go Documentation**
  https://pkg.go.dev/github.com/quic-go/quic-go

- **qlog**: QUIC logging for debugging
  https://github.com/quic-go/qlog

- **qvis**: QUIC visualization tool
  https://qvis.quictools.info/

### TLS Certificates

- **Go crypto/tls Documentation**
  https://pkg.go.dev/crypto/tls

- **Let's Encrypt**: Free TLS certificates
  https://letsencrypt.org/

- **mkcert**: Local development certificates
  https://github.com/FiloSottile/mkcert

### Related Work

- **Caddy Server**: Production QUIC usage in Go
  https://caddyserver.com/docs/quic

- **IPFS + QUIC**: Decentralized systems using QUIC
  https://github.com/libp2p/specs/blob/master/quic/README.md

- **Cloudflare QUIC Blog Posts**
  https://blog.cloudflare.com/tag/quic/

### Mycorrizal Project Files

- `CLAUDE.md`: Project overview and architecture
- `internal/nodosum/`: Current networking implementation
- `internal/mycel/`: Cache layer using Nodosum

---

## Appendix: Configuration Examples

### Development Configuration

```go
// For local development with self-signed certs
func GetDevelopmentConfig() *Config {
    // Generate self-signed cert (use mkcert or crypto/tls)
    cert, _ := generateSelfSignedCert("localhost")

    return &Config{
        NodeID:       uuid.New().String(),
        ConnectPort:  "7946",
        DiscoveryMode: DC_MODE_STATIC,
        NodeAddrs:    []string{"localhost:7946"},

        TlsCert:   &cert,
        TlsCACert: createCertPool(cert),

        QuicConfig: &quic.Config{
            MaxIdleTimeout:     30 * time.Second,
            KeepAlivePeriod:    10 * time.Second,
            MaxIncomingStreams: 100,
        },

        ALPNProtocol: "mycorrizal-quic-dev",

        MultiplexerWorkerCount: 4,
        MultiplexerBufferSize:  1000,
    }
}
```

### Production Configuration

```go
// For production with proper certificates
func GetProductionConfig() *Config {
    // Load certificates from files
    cert, _ := tls.LoadX509KeyPair("server.crt", "server.key")
    caCert, _ := os.ReadFile("ca.crt")
    caPool := x509.NewCertPool()
    caPool.AppendCertsFromPEM(caCert)

    return &Config{
        NodeID:        os.Getenv("MYCORRIZAL_ID"),
        ConnectPort:   "7946",
        DiscoveryMode: DC_MODE_DNS_SD,
        DiscoveryHost: "mycorrizal.service.consul",

        TlsCert:   &cert,
        TlsCACert: caPool,

        QuicConfig: &quic.Config{
            MaxIdleTimeout:     60 * time.Second,
            KeepAlivePeriod:    20 * time.Second,
            MaxIncomingStreams: 1000,
        },

        ALPNProtocol: "mycorrizal-quic-v1",

        MultiplexerWorkerCount: runtime.NumCPU(),
        MultiplexerBufferSize:  10000,

        LogLevel: "info",
    }
}
```

### Certificate Generation Script

```bash
#!/bin/bash
# generate_certs.sh - Generate certificates for Mycorrizal cluster

set -e

# Configuration
CA_KEY="ca-key.pem"
CA_CERT="ca-cert.pem"
NODE_KEY="node-key.pem"
NODE_CERT="node-cert.pem"
NODE_ID="${1:-$(uuidgen)}"

# Generate CA
echo "Generating CA certificate..."
openssl req -new -x509 -nodes -days 365 \
  -keyout "$CA_KEY" \
  -out "$CA_CERT" \
  -subj "/CN=Mycorrizal CA"

# Generate node key
echo "Generating node key for $NODE_ID..."
openssl genrsa -out "$NODE_KEY" 2048

# Generate node CSR
openssl req -new -key "$NODE_KEY" \
  -out node.csr \
  -subj "/CN=$NODE_ID"

# Sign node certificate
echo "Signing node certificate..."
openssl x509 -req -in node.csr \
  -CA "$CA_CERT" \
  -CAkey "$CA_KEY" \
  -CAcreateserial \
  -out "$NODE_CERT" \
  -days 365 \
  -sha256

# Cleanup
rm node.csr

echo "Certificates generated:"
echo "  CA cert: $CA_CERT"
echo "  Node cert: $NODE_CERT"
echo "  Node key: $NODE_KEY"
```

---

## Appendix: Migration Checklist

### Pre-Migration

- [ ] Review this plan with team
- [ ] Assign developers to migration phases
- [ ] Set up development environment with QUIC
- [ ] Generate test certificates for dev cluster
- [ ] Create feature branch: `feature/quic-migration`

### Week 1: Foundation

- [ ] Add quic-go dependency
- [ ] Update Config struct
- [ ] Implement `listenQuic()`
- [ ] Implement `acceptConnections()`
- [ ] Implement basic connection registration
- [ ] Test: 2 nodes can establish QUIC connection
- [ ] Test: mTLS authentication works
- [ ] Commit: "feat: replace TCP/UDP listeners with QUIC"

### Week 2: Streams

- [ ] Implement `acceptStreams()`
- [ ] Implement `handleStream()`
- [ ] Implement stream init protocol
- [ ] Implement `getOrCreateStream()`
- [ ] Implement `sendToStream()`
- [ ] Update `protocol.go` with new framing
- [ ] Test: Stream creation and messaging
- [ ] Test: Multiple streams per connection
- [ ] Commit: "feat: implement QUIC stream management"

### Week 3: Integration

- [ ] Update multiplexer outbound
- [ ] Update multiplexer inbound
- [ ] Remove old read/write loops
- [ ] Update connection registry
- [ ] Implement discovery dialing
- [ ] Remove UDP handshake code
- [ ] Test: Multi-node cluster formation
- [ ] Test: Application messaging works
- [ ] Commit: "feat: integrate QUIC with multiplexer and discovery"

### Week 4: Testing & Polish

- [ ] Write unit tests (protocol, conn, mux, app)
- [ ] Write integration tests (cluster formation, messaging)
- [ ] Run benchmarks (compare to baseline)
- [ ] Fix performance issues identified
- [ ] Update documentation (CLAUDE.md)
- [ ] Add configuration examples
- [ ] Code review
- [ ] Commit: "test: comprehensive QUIC test suite"
- [ ] Commit: "docs: update architecture for QUIC"

### Deployment

- [ ] Merge to main
- [ ] Deploy to dev environment
- [ ] Deploy to staging
- [ ] Run load tests on staging
- [ ] Deploy to production (rolling)
- [ ] Monitor production metrics
- [ ] Document lessons learned

---

---

## Conclusion & Final Recommendation

### Summary of Hybrid Architecture Benefits

The **Gossip (Memberlist) + QUIC** hybrid architecture addresses all major production readiness gaps in the current Nodosum implementation:

**Critical Improvements**:
1. ✅ **Failure Detection**: SWIM probes replace passive TCP detection (1-5s vs. minutes)
2. ✅ **Split-brain Protection**: Gossip propagates partition awareness cluster-wide
3. ✅ **Observability**: Built-in Prometheus metrics + event callbacks
4. ✅ **Code Quality**: ~900 lines eliminated, battle-tested libraries
5. ✅ **Scalability**: Proven to thousands of nodes (Consul production use)
6. ✅ **Stream Multiplexing**: QUIC eliminates head-of-line blocking
7. ✅ **Security**: Mandatory encryption (gossip AES-GCM + QUIC TLS 1.3)
8. ✅ **Connection Resilience**: QUIC migration survives IP changes (K8s pods)

### Why This is Better Than QUIC-Only

| Concern | QUIC-Only | Gossip + QUIC |
|---------|-----------|---------------|
| Failure detection | Passive (connection breaks) | **Active SWIM probes** |
| Membership propagation | Must dial all nodes | **O(log n) gossip** |
| Split-brain awareness | None | **Gossip detects partitions** |
| Health monitoring | None | **Configurable probes** |
| Code to maintain | ~1,100 lines custom | **~600 lines custom** |
| Production maturity | Good | **Excellent** |

### Investment vs. Return

**Investment**: 6 weeks development + testing
**Return**:
- 60% less custom code to maintain
- Production-grade reliability (Consul-proven)
- Rich observability (metrics, events, health)
- Future-proof architecture (standard protocols)
- Reduced operational risk

### When to Start

**Recommend starting when**:
- ✅ You have 6 weeks of dedicated development time
- ✅ Team has reviewed and approved this plan
- ✅ Development environment prepared
- ✅ Certificate infrastructure planned

**Prerequisites**:
- Understanding of SWIM protocol basics (read SWIM paper)
- Familiarity with QUIC concepts
- Test environment with multiple nodes

### Success Metrics

After migration, Mycorrizal will achieve:
- **< 5 seconds** failure detection (vs. minutes)
- **< 0.1%** false positive rate (vs. high current rate)
- **1000+ nodes** supported cluster size (vs. unknown)
- **Full observability** with Prometheus metrics
- **Zero** known production gaps

---

**End of Migration Plan**

**Next Steps**:
1. Review this plan with team/stakeholders
2. Read SWIM paper and Memberlist documentation
3. Decide on timeline to begin implementation
4. Set up development environment with certificates
5. Create feature branch: `feature/gossip-quic-migration`
6. Begin Phase 1: Memberlist Integration

**Questions or Concerns**: Document in GitHub issues for tracking and discussion.

**Key Contacts**:
- HashiCorp Memberlist: https://github.com/hashicorp/memberlist/issues
- quic-go: https://github.com/quic-go/quic-go/issues
- Consul Community (Memberlist users): https://discuss.hashicorp.com/c/consul
