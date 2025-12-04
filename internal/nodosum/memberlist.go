package nodosum

import (
	"context"
	"fmt"
	"net"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/hashicorp/memberlist"
)

type Delegate struct {
	*Nodosum
}

func (d Delegate) NotifyJoin(node *memberlist.Node) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("panic in NotifyJoin recovered", "panic", r, "node", node.Name)
			debug.PrintStack()
		}
	}()

	d.quicConns.RLock()
	if _, exists := d.quicConns.conns[node.Name]; exists {
		d.quicConns.RUnlock()
		d.logger.Debug(fmt.Sprintf("quic connection exists: %s", node.Name))
		return
	}
	d.quicConns.RUnlock()

	addr, err := net.ResolveUDPAddr("udp", node.Addr.String()+":"+strconv.Itoa(d.quicPort))
	if err != nil {
		d.logger.Error(fmt.Sprintf("error resolving quic address: %s", err.Error()))
	}

	dialCtx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	conn, err := d.quicTransport.Dial(dialCtx, addr, d.tlsConfig, d.quicConfig)
	if err != nil {
		d.logger.Error(fmt.Sprintf("error accepting quic connection: %s", err.Error()))
		return
	}

	// Check with write lock to eliminate creating 2 connections through a race condition
	d.quicConns.Lock()
	if _, exists := d.quicConns.conns[node.Name]; exists {
		d.quicConns.Unlock()
		if conn != nil {
			err = conn.CloseWithError(0, "duplicate quic connection")
			if err != nil {
				d.logger.Error(fmt.Sprintf("error closing duplicate quic connection: %s", err.Error()))
			}
		}
		d.logger.Debug(fmt.Sprintf("duplicate quic connection: %s", node.Name))
		return
	}
	d.quicConns.conns[node.Name] = conn
	d.quicConns.Unlock()

	d.logger.Info("quic connection established",
		"node", node.Name,
		"remoteAddr", conn.RemoteAddr().String(),
		"serverName", conn.ConnectionState().TLS.ServerName)
}

func (d Delegate) NotifyLeave(node *memberlist.Node) {

	d.quicConns.Lock()

	c := d.quicConns.conns[node.Name]
	if c != nil {
		err := c.CloseWithError(0, "goodbye")
		if err != nil {
			d.logger.Error(err.Error())
		}
	}

	delete(d.quicConns.conns, node.Name)
	d.quicConns.Unlock()

	d.logger.Debug(fmt.Sprintf("node left: %s", node.Name))
}

func (d Delegate) NotifyUpdate(node *memberlist.Node) {
	d.logger.Debug(fmt.Sprintf("node changed state: %s", node.Name))
}
