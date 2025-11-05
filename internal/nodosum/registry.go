package nodosum

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/conamu/go-worker"
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

type NodeMetaMap struct {
	Mu  sync.Mutex
	IPs []string
	// Map ID to NodeMeta
	Map map[string]NodeMeta
}

type NodeMeta struct {
	Addr        string
	ConnectPort string
	ID          string
	alive       bool
}

func (n *Nodosum) createConnChannel(id string, conn net.Conn) {
	ctx, cancel := context.WithCancel(n.ctx)

	n.nodeMeta.Mu.Lock()

	ipPort := strings.Split(conn.RemoteAddr().String(), ":")
	nm := NodeMeta{
		Addr:        ipPort[0],
		ConnectPort: ipPort[1],
		ID:          id,
		alive:       true,
	}
	n.nodeMeta.Map[id] = nm

	n.nodeMeta.Mu.Unlock()

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
		n.nodeMeta.Mu.Lock()
		nm := n.nodeMeta.Map[id]
		nm.alive = false
		n.nodeMeta.Mu.Unlock()
	}
	n.connections.Delete(id)
}

func (n *Nodosum) nodeAppSyncTask(w *worker.Worker, msg any) {
	nodes := []string{}
	n.nodeMeta.Mu.Lock()
	for _, meta := range n.nodeMeta.Map {
		if !meta.alive {
			continue
		}
		nodes = append(nodes, meta.ID)
	}
	n.nodeMeta.Mu.Unlock()

	n.applications.Range(func(key, value any) bool {
		app := value.(*application)
		app.nodes = nodes
		return true
	})
}
