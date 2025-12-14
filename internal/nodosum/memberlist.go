package nodosum

import (
	"context"
	"net"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

type Delegate struct {
	*Nodosum
	dialAttempts   map[string]int
	dialAttemptsMu *sync.RWMutex
}

func (d Delegate) NotifyJoin(node *memberlist.Node) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("panic in NotifyJoin recovered", "panic", r, "node", node.Name)
			debug.PrintStack()
		}
	}()

	d.logger.Debug("NotifyJoin called", "node", node.Name, "addr", node.Addr.String())

	d.quicConns.RLock()
	if _, exists := d.quicConns.conns[node.Name]; exists {
		d.quicConns.RUnlock()
		d.logger.Debug("quic connection already exists", "node", node.Name)
		// Reset dial attempts on successful existing connection
		d.dialAttemptsMu.Lock()
		delete(d.dialAttempts, node.Name)
		d.dialAttemptsMu.Unlock()
		return
	}
	d.quicConns.RUnlock()

	addr, err := net.ResolveUDPAddr("udp", node.Addr.String()+":"+strconv.Itoa(d.quicPort))
	if err != nil {
		d.logger.Error("error resolving quic address", "error", err, "node", node.Name)
		return
	}

	dialCtx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	conn, err := d.quicTransport.Dial(dialCtx, addr, d.tlsConfig, d.quicConfig)
	if err != nil {
		// Track failed dial attempts for exponential backoff
		d.dialAttemptsMu.Lock()
		if d.dialAttempts == nil {
			d.dialAttempts = make(map[string]int)
		}
		d.dialAttempts[node.Name]++
		attempts := d.dialAttempts[node.Name]
		d.dialAttemptsMu.Unlock()

		d.logger.Error("error dialing quic connection", "error", err, "node", node.Name, "addr", addr.String(), "attempts", attempts)

		// Retry with exponential backoff (max 5 attempts, then rely on AliveDelegate)
		if attempts < 5 {
			backoff := time.Duration(attempts*attempts) * 100 * time.Millisecond
			d.logger.Debug("scheduling retry", "node", node.Name, "backoff", backoff, "attempt", attempts+1)
			time.AfterFunc(backoff, func() {
				d.NotifyJoin(node)
			})
		} else {
			d.logger.Warn("max dial attempts reached, will retry via AliveDelegate", "node", node.Name)
		}
		return
	}

	// Check with write lock to eliminate creating 2 connections through a race condition
	d.quicConns.Lock()
	if _, exists := d.quicConns.conns[node.Name]; exists {
		d.quicConns.Unlock()
		if conn != nil {
			err = conn.CloseWithError(0, "duplicate quic connection")
			if err != nil {
				d.logger.Error("error closing duplicate quic connection", "error", err, "node", node.Name)
			}
		}
		d.logger.Debug("duplicate quic connection", "node", node.Name)
		return
	}
	d.quicConns.conns[node.Name] = conn
	d.quicConns.Unlock()

	// Reset dial attempts on success
	d.dialAttemptsMu.Lock()
	delete(d.dialAttempts, node.Name)
	d.dialAttemptsMu.Unlock()

	d.logger.Info("quic connection established",
		"remoteServerName", node.Name,
		"remoteAddr", conn.RemoteAddr().String(),
		"localTlsServerName", conn.ConnectionState().TLS.ServerName)
}

func (d Delegate) NotifyLeave(node *memberlist.Node) {
	d.closeQuicConnection(node.Name)
	// Clear dial attempts for this node
	d.dialAttemptsMu.Lock()
	delete(d.dialAttempts, node.Name)
	d.dialAttemptsMu.Unlock()
	d.logger.Debug("node left", "node", node.Name)
}

func (d Delegate) NotifyUpdate(node *memberlist.Node) {
	d.logger.Debug("node metadata updated", "node", node.Name, "addr", node.Addr.String())

	// Close existing connection and reconnect with new metadata
	// This handles cases where node IP/port changes (rolling updates, network reconfig)
	d.quicConns.Lock()
	if conn, exists := d.quicConns.conns[node.Name]; exists {
		d.logger.Info("reconnecting to node with updated metadata", "node", node.Name, "newAddr", node.Addr.String())
		if conn != nil {
			conn.CloseWithError(0, "node metadata updated")
		}
		delete(d.quicConns.conns, node.Name)
	}
	d.quicConns.Unlock()

	// Reset dial attempts for clean retry
	d.dialAttemptsMu.Lock()
	delete(d.dialAttempts, node.Name)
	d.dialAttemptsMu.Unlock()

	// Reconnect with new metadata
	go d.NotifyJoin(node)
}

// NotifyAlive is called when memberlist confirms a node is alive during gossip
// This provides periodic health checks and recovery from temporary failures
func (d Delegate) NotifyAlive(node *memberlist.Node) error {
	// Skip self
	if node.Name == d.nodeId {
		return nil
	}

	d.quicConns.RLock()
	conn, exists := d.quicConns.conns[node.Name]
	d.quicConns.RUnlock()

	// If no connection exists, try to establish one
	if !exists {
		d.logger.Debug("NotifyAlive: no QUIC connection exists, attempting to establish", "node", node.Name)
		go d.NotifyJoin(node)
		return nil
	}

	// Verify connection is still alive by checking context state
	if conn.Context().Err() != nil {
		d.logger.Warn("NotifyAlive: QUIC connection context cancelled, reconnecting", "node", node.Name, "error", conn.Context().Err())
		d.closeQuicConnection(node.Name)
		go d.NotifyJoin(node)
		return nil
	}

	// Connection exists and is healthy
	d.logger.Debug("NotifyAlive: QUIC connection healthy", "node", node.Name)
	return nil
}

// NotifyMerge is called when two separate clusters merge (network partition heals)
// Establish QUIC connections to all nodes from the other cluster
func (d Delegate) NotifyMerge(peers []*memberlist.Node) error {
	d.logger.Info("cluster merge detected, establishing QUIC connections", "peerCount", len(peers))

	for _, peer := range peers {
		if peer.Name != d.nodeId {
			d.logger.Debug("establishing connection to merged peer", "node", peer.Name, "addr", peer.Addr.String())
			go d.NotifyJoin(peer)
		}
	}

	return nil
}
