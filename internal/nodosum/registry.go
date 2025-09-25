package nodosum

import (
	"context"
	"errors"
	"net"
)

type nodeConn struct {
	connId    string
	addr      net.Addr
	ctx       context.Context
	cancel    context.CancelFunc
	conn      net.Conn
	readChan  chan []byte
	writeChan chan []byte
}

func (n *Nodosum) createNewChannel(id string, conn net.Conn) {
	ctx, cancel := context.WithCancel(n.ctx)

	n.connections.Store(id, &nodeConn{
		connId:    id,
		addr:      conn.RemoteAddr(),
		conn:      conn,
		ctx:       ctx,
		cancel:    cancel,
		readChan:  make(chan []byte),
		writeChan: make(chan []byte),
	})
}

func (n *Nodosum) closeConnChannel(id string) {
	n.logger.Debug("closing connection channel for " + id)
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
