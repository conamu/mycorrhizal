package nodosum

import (
	"context"
	"errors"
	"net"
)

type node struct {
	id       string
	addr     *net.TCPAddr
	connChan *nodeConnChannel
}

type nodeConnChannel struct {
	connId    string
	ctx       context.Context
	cancel    context.CancelFunc
	conn      net.Conn
	readChan  chan []byte
	writeChan chan []byte
}

func (n *Nodosum) createNewChannel(id string, conn net.Conn) {
	ctx, cancel := context.WithCancel(n.ctx)

	n.channelRegistry.Store(id, &nodeConnChannel{
		connId:    id,
		conn:      conn,
		ctx:       ctx,
		cancel:    cancel,
		readChan:  make(chan []byte),
		writeChan: make(chan []byte),
	})
}

func (n *Nodosum) closeConnChannel(id string) {
	n.logger.Debug("closing connection channel for " + id)
	c, ok := n.channelRegistry.Load(id)
	if ok {
		conn := c.(*nodeConnChannel)
		conn.cancel()
		err := conn.conn.Close()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			n.logger.Error("error closing comms channels for", "error", err.Error())
		}
	}
	n.channelRegistry.Delete(id)
}
