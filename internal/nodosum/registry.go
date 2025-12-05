package nodosum

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/conamu/go-worker"
	"github.com/quic-go/quic-go"
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
	sync.Mutex
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

func (n *Nodosum) getOrOpenQuicStream(nodeId, app, name string) (*quic.Stream, error) {
	key := fmt.Sprintf("%s:%s:%s", nodeId, app, name)

	n.quicApplicationStreams.RLock()
	if stream, ok := n.quicApplicationStreams.streams[key]; ok {
		n.quicApplicationStreams.RUnlock()
		return stream, nil
	}
	n.quicApplicationStreams.RUnlock()

	n.quicConns.RLock()
	if conn, ok := n.quicConns.conns[nodeId]; ok {
		n.quicConns.RUnlock()

		stream, err := conn.OpenStreamSync(n.ctx)
		if err != nil {
			return nil, err
		}

		streamInitFrame := encodeStreamInit(nodeId, app, name)
		b, err := stream.Write(streamInitFrame)
		if err != nil {
			n.logger.Error(fmt.Sprintf("error writing initial quic stream: %s", err.Error()))
		}
		if b != len(streamInitFrame) {
			n.logger.Warn(fmt.Sprintf("initial quic stream written bytes wrong, expected %d, got %d", len(streamInitFrame), b))
		}

		n.quicApplicationStreams.Lock()
		n.quicApplicationStreams.streams[key] = stream
		n.quicApplicationStreams.Unlock()
		return stream, nil
	}

	return nil, errors.New(fmt.Sprintf("quic connection to node %s could not be found, not creating stream for key %s", nodeId, key))
}

func (n *Nodosum) closeQuicStream(id string) {
	stream, ok := n.quicApplicationStreams.streams[id]
	if !ok {
		n.logger.Warn(fmt.Sprintf("closing quic stream for %s, but stream is nil", id))
		return
	}

	err := stream.Close()
	if err != nil {
		n.logger.Error(fmt.Sprintf("error closing quic stream: %s", err.Error()))
	}
	delete(n.quicApplicationStreams.streams, id)
	return
}

func (n *Nodosum) closeAllQuicStreams() {
	n.quicApplicationStreams.Lock()
	defer n.quicApplicationStreams.Unlock()

	for id, stream := range n.quicApplicationStreams.streams {
		err := stream.Close()
		if err != nil {
			n.logger.Error(fmt.Sprintf("error closing quic stream: %s", err.Error()))
		}
		delete(n.quicApplicationStreams.streams, id)
	}
}

// Old shit

func (n *Nodosum) createConnChannel(id string, conn net.Conn) {
	ctx, cancel := context.WithCancel(n.ctx)
	conn.SetReadDeadline(time.Time{})

	n.nodeMeta.Lock()

	ipPort := strings.Split(conn.RemoteAddr().String(), ":")
	nm := NodeMeta{
		Addr:        ipPort[0],
		ConnectPort: ipPort[1],
		ID:          id,
		alive:       true,
	}
	n.nodeMeta.Map[id] = nm

	n.nodeMeta.Unlock()

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
		n.nodeMeta.Lock()
		nm := n.nodeMeta.Map[id]
		nm.alive = false
		n.nodeMeta.Map[id] = nm
		n.nodeMeta.Unlock()
	}
	n.connections.Delete(id)
}

func (n *Nodosum) nodeAppSyncTask(w *worker.Worker, msg any) {
	nodes := []string{}
	n.nodeMeta.Lock()
	for _, meta := range n.nodeMeta.Map {
		if !meta.alive {
			continue
		}
		nodes = append(nodes, meta.ID)
	}
	n.nodeMeta.Unlock()

	n.applications.Range(func(key, value any) bool {
		app := value.(*application)
		app.nodes = nodes
		return true
	})
}
