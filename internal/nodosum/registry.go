package nodosum

import (
	"errors"
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
)

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
