package nodosum

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/quic-go/quic-go"
)

func (n *Nodosum) listenQuic() {
	ln, err := n.quicTransport.Listen(n.tlsConfig, n.quicConfig)
	if err != nil {
		n.logger.Error(fmt.Sprintf("error listening for quic connections: %s", err.Error()))
	}

	n.wg.Go(func() {
		<-n.ctx.Done()
		err := ln.Close()
		if err != nil {
			n.logger.Error(fmt.Sprintf("error closing quic listener: %s", err.Error()))
		}
		n.logger.Info(fmt.Sprintf("quic listener closed"))
	})

	for {
		select {
		case <-n.ctx.Done():
			n.logger.Debug("quic listener accept loop stopped")
			return
		default:
			conn, err := ln.Accept(n.ctx)
			if err != nil {
				// Check if this is a shutdown error
				if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
					n.logger.Debug("quic listener shutting down")
					return
				}
				n.logger.Error(fmt.Sprintf("error accepting quic connection: %s", err.Error()))
				continue
			}
			go n.handleQuicConn(conn)
		}
	}

}

func (n *Nodosum) handleQuicConn(conn *quic.Conn) {
	if conn == nil {
		n.logger.Error("handleQuicConn called with nil connection")
		return
	}
	nodeID := conn.ConnectionState().TLS.ServerName

	for {
		select {
		case <-n.ctx.Done():
			n.logger.Debug("handleQuicConn exiting - context cancelled", "nodeID", nodeID)
			return
		default:
			stream, err := conn.AcceptStream(n.ctx)
			if err != nil {
				// Check if connection is closed
				if isQuicConnectionClosed(err) {
					n.logger.Debug("connection closed, stopping accept loop",
						"nodeID", nodeID, "error", err.Error())
					return
				}

				// Temporary error - log and continue
				n.logger.Warn("temporary error accepting stream",
					"nodeID", nodeID, "error", err.Error())
				continue
			}

			go n.handleStream(stream)
		}
	}
}

func (n *Nodosum) closeQuicConnection(nodeId string) {

	// Clean up all streams for this node
	n.quicApplicationStreams.Lock()
	keysToDelete := make([]string, 0)
	for key := range n.quicApplicationStreams.streams {
		// Key format: "nodeId:app:name"
		if strings.HasPrefix(key, nodeId+":") {
			keysToDelete = append(keysToDelete, key)
		}
	}
	for _, key := range keysToDelete {
		if stream := n.quicApplicationStreams.streams[key]; stream != nil {
			err := (*stream).Close()
			if err != nil {
				n.logger.Error(fmt.Sprintf("error closing quic stream: %s", err.Error()))
			}
		}
		delete(n.quicApplicationStreams.streams, key)
	}
	n.quicApplicationStreams.Unlock()

	// Close the QUIC connection
	n.quicConns.Lock()
	if conn, exists := n.quicConns.conns[nodeId]; exists {
		if conn != nil {
			err := conn.CloseWithError(0, "connection closed")
			if err != nil {
				n.logger.Error(fmt.Sprintf("error closing quic connection: %s", err.Error()))
			}
		}
		delete(n.quicConns.conns, nodeId)
	}
	n.quicConns.Unlock()

	n.logger.Debug("closed QUIC connection and all streams", "nodeId", nodeId, "streamsClosed", len(keysToDelete))
}

func (n *Nodosum) handleStream(stream *quic.Stream) {
	if stream == nil {
		return
	}

	nodeId, appId, streamName, err := decodeStreamInit(stream)
	if err != nil {
		n.logger.Error(fmt.Sprintf("error decoding quic initial frame: %s", err.Error()))
	}

	key := fmt.Sprintf("%s:%s:%s", nodeId, appId, streamName)

	n.quicApplicationStreams.Lock()
	n.quicApplicationStreams.streams[key] = stream
	n.quicApplicationStreams.Unlock()

	go n.streamReadLoop(stream, nodeId, appId)

	n.logger.Debug(fmt.Sprintf("Registered stream, %s, %s, %s", nodeId, appId, streamName))
}

func (n *Nodosum) streamReadLoop(stream *quic.Stream, nodeID, appID string) {
	defer stream.Close()

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
			// Read next frame using existing readFrame() function from quic.go
			frameType, payload, err := readFrame(stream)
			if err != nil {
				if isQuicConnectionClosed(err) {
					n.logger.Debug("stream closed", "nodeID", nodeID, "appID", appID)
					return
				}
				n.logger.Error("error reading frame", "error", err)
				return
			}

			// For now, only handle DATA frames (REQUEST/RESPONSE not yet implemented)
			switch frameType {
			case FRAME_TYPE_DATA:
				n.routeToApplication(appID, payload, nodeID)
			default:
				n.logger.Warn("unknown frame type", "type", frameType)
			}
		}
	}
}

func (n *Nodosum) streamWriteLoop() {}

func isQuicConnectionClosed(err error) bool {
	if err == nil {
		return false
	}

	// QUIC-specific errors
	var appErr *quic.ApplicationError
	var idleErr *quic.IdleTimeoutError
	var statelessResetErr *quic.StatelessResetError

	return errors.As(err, &appErr) ||
		errors.As(err, &idleErr) ||
		errors.As(err, &statelessResetErr) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "timeout: no recent network activity")
}
