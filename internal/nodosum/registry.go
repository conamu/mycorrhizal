package nodosum

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

type nodeConn struct {
	connId    string
	addr      net.Addr
	ctx       context.Context
	cancel    context.CancelFunc
	conn      net.Conn
	readChan  chan any
	writeChan chan any
}

type NodeAddrs struct {
	Mu      sync.Mutex
	IpIdMap map[string]string
}

func (n *Nodosum) createConnChannel(id string, conn net.Conn) {
	ctx, cancel := context.WithCancel(n.ctx)

	n.connections.Store(id, &nodeConn{
		connId:    id,
		addr:      conn.RemoteAddr(),
		conn:      conn,
		ctx:       ctx,
		cancel:    cancel,
		readChan:  n.globalReadChannel,
		writeChan: make(chan any, 100),
	})
}

func (n *Nodosum) closeConnChannel(id string) {
	n.logger.Debug(fmt.Sprintf("closing connection channel for %s", id))
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
