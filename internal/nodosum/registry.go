package nodosum

import (
	"context"
	"errors"
	"fmt"
	"net"
)

type nodeConn struct {
	connId    uint32
	addr      net.Addr
	ctx       context.Context
	cancel    context.CancelFunc
	conn      net.Conn
	readChan  chan any
	writeChan chan any
}

func (n *Nodosum) createConnChannel(id uint32, conn net.Conn) {
	ctx, cancel := context.WithCancel(n.ctx)

	n.connections.Store(id, &nodeConn{
		connId:    id,
		addr:      conn.RemoteAddr(),
		conn:      conn,
		ctx:       ctx,
		cancel:    cancel,
		readChan:  n.globalReadChannel,
		writeChan: make(chan any),
	})
}

func (n *Nodosum) closeConnChannel(id uint32) {
	n.logger.Debug(fmt.Sprintf("closing connection channel for %d", id))
	c, ok := n.connections.Load(id)
	if ok {
		conn := c.(*nodeConn)
		conn.cancel()
		err := conn.conn.Close()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			n.logger.Error("error closing comms channels for", "error", err.Error())
		}
	}
	n.connections.Delete(id)
}
