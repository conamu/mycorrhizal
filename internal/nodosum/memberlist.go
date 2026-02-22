package nodosum

import (
	"context"
	"encoding/binary"
	"net"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

type Delegate struct {
	*Nodosum
	dta *delegateDialAttempts
}

type delegateDialAttempts struct {
	sync.RWMutex
	att map[string]int
}

func (d Delegate) NotifyJoin(node *memberlist.Node) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("panic in NotifyJoin recovered", "panic", r, "node", node.Name)
			debug.PrintStack()
		}
	}()

	if node.Name == d.nodeId {
		d.logger.Debug("node is self, skipping NodeJoin()")
		return
	}

	d.logger.Debug("NotifyJoin called", "node", node.Name, "addr", node.Addr.String())

	d.quicConns.RLock()
	if _, exists := d.quicConns.conns[node.Name]; exists {
		d.quicConns.RUnlock()
		d.logger.Debug("quic connection already exists", "node", node.Name)
		// Reset dial attempts on successful existing connection
		d.dta.Lock()
		delete(d.dta.att, node.Name)
		d.dta.Unlock()
		return
	}
	d.quicConns.RUnlock()

	quicPort := binary.BigEndian.Uint16(node.Meta[:2])

	addr, err := net.ResolveUDPAddr("udp", node.Addr.String()+":"+strconv.Itoa(int(quicPort)))
	if err != nil {
		d.logger.Error("error resolving quic address", "error", err, "node", node.Name)
		return
	}

	dialCtx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()

	// Create TLS config with the remote node's ID as ServerName for proper certificate verification
	dialTLSConfig := d.createDialTLSConfig(node.Name)
	conn, err := d.quicTransport.Dial(dialCtx, addr, dialTLSConfig, d.quicConfig)
	if err != nil {
		// Track failed dial attempts for exponential backoff
		d.dta.Lock()
		if d.dta == nil {
			d.dta.att = make(map[string]int)
		}
		d.dta.att[node.Name]++
		attempts := d.dta.att[node.Name]
		d.dta.Unlock()

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
	d.dta.Lock()
	delete(d.dta.att, node.Name)
	d.dta.Unlock()

	// Notify topology hook (async to avoid blocking memberlist callback)
	if d.Nodosum.onTopologyChange != nil {
		go d.Nodosum.onTopologyChange(node.Name, true)
	}

	d.logger.Info("quic connection established",
		"remoteNodeID", node.Name,
		"remoteAddr", conn.RemoteAddr().String(),
		"tlsServerName", conn.ConnectionState().TLS.ServerName,
		"peerCertCN", conn.ConnectionState().TLS.PeerCertificates[0].Subject.CommonName)
}

func (d Delegate) NotifyLeave(node *memberlist.Node) {
	d.closeQuicConnection(node.Name)
	// Clear dial attempts for this node
	d.dta.Lock()
	delete(d.dta.att, node.Name)
	d.dta.Unlock()
	d.logger.Debug("node left", "node", node.Name)

	// Notify topology hook (async to avoid blocking memberlist callback)
	if d.Nodosum.onTopologyChange != nil {
		go d.Nodosum.onTopologyChange(node.Name, false)
	}
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
	d.dta.Lock()
	delete(d.dta.att, node.Name)
	d.dta.Unlock()

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

// NodeMeta is used to exchange quic connection info
func (d Delegate) NodeMeta(limit int) []byte {
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(d.quicPort))
	return portBytes
}

func (d Delegate) NotifyMsg(bytes []byte) {
	return
}

func (d Delegate) GetBroadcasts(overhead, limit int) [][]byte {
	return nil
}

func (d Delegate) LocalState(join bool) []byte {
	return nil
}

func (d Delegate) MergeRemoteState(buf []byte, join bool) {
	return
}
