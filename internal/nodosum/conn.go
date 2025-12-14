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

	// Get remote node ID from peer certificate's CommonName
	tlsState := conn.ConnectionState().TLS
	if len(tlsState.PeerCertificates) == 0 {
		n.logger.Error("no peer certificates in connection")
		return
	}
	nodeID := tlsState.PeerCertificates[0].Subject.CommonName

	// Register this connection so we can send messages back to this node
	// Only register if no connection exists (prefer outgoing dial connections)
	shouldRegister := false
	n.quicConns.Lock()
	if _, exists := n.quicConns.conns[nodeID]; !exists {
		n.quicConns.conns[nodeID] = conn
		shouldRegister = true
		n.quicConns.Unlock()
		n.logger.Info("registered incoming quic connection", "nodeID", nodeID, "remoteAddr", conn.RemoteAddr().String())
	} else {
		n.quicConns.Unlock()
		n.logger.Debug("incoming connection not registered, outgoing connection already exists", "nodeID", nodeID)
	}

	// Clean up connection from map when this handler exits (only if we registered it)
	defer func() {
		if shouldRegister {
			n.quicConns.Lock()
			if n.quicConns.conns[nodeID] == conn {
				delete(n.quicConns.conns, nodeID)
			}
			n.quicConns.Unlock()
			n.logger.Debug("unregistered quic connection", "nodeID", nodeID)
		}
	}()

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

	// Read frame type byte (should be FRAME_TYPE_STREAM_INIT)
	typeBuf := make([]byte, 1)
	_, err := io.ReadFull(stream, typeBuf)
	if err != nil {
		n.logger.Error("error reading stream init frame type", "error", err)
		return
	}

	if typeBuf[0] != FRAME_TYPE_STREAM_INIT {
		n.logger.Error("unexpected frame type in stream init", "type", typeBuf[0])
		return
	}

	nodeId, appId, streamName, err := decodeStreamInit(stream)
	if err != nil {
		n.logger.Error("error decoding quic initial frame", "error", err)
		return
	}

	key := fmt.Sprintf("%s:%s:%s", nodeId, appId, streamName)

	n.quicApplicationStreams.Lock()
	n.quicApplicationStreams.streams[key] = stream
	n.quicApplicationStreams.Unlock()

	go n.streamReadLoop(stream, nodeId, appId)

	n.logger.Debug("Registered stream", "nodeId", nodeId, "appId", appId, "streamName", streamName)
}

func (n *Nodosum) handleRequest(stream *quic.Stream, appID string, frameData []byte, fromNode string) {
	// Decode request frame
	var reqFrame RequestFrame
	err := decodeRequestFrame(frameData, &reqFrame)
	if err != nil {
		n.logger.Error("failed to decode request frame", "error", err)
		return
	}

	// Get application
	n.applications.RLock()
	app, exists := n.applications.applications[appID]
	n.applications.RUnlock()

	if !exists || app.requestHandler == nil {
		// Send error response
		respFrame := ResponseFrame{
			CorrelationID: reqFrame.CorrelationID,
			Error:         "application not found or no request handler",
		}
		sendResponseFrame(stream, respFrame)
		return
	}

	// Call handler
	respPayload, err := app.requestHandler(reqFrame.Payload, fromNode)

	// Send response
	respFrame := ResponseFrame{
		CorrelationID: reqFrame.CorrelationID,
		Payload:       respPayload,
	}
	if err != nil {
		respFrame.Error = err.Error()
	}

	sendResponseFrame(stream, respFrame)
}

func (n *Nodosum) handleResponse(frameData []byte) {
	// Decode response frame
	var respFrame ResponseFrame
	err := decodeResponseFrame(frameData, &respFrame)
	if err != nil {
		n.logger.Error("failed to decode response frame", "error", err)
		return
	}

	// Find pending request
	// Note: This needs access to all applications' pending requests
	// Consider moving pendingRequests to Nodosum level
	n.applications.RLock()
	defer n.applications.RUnlock()

	for _, app := range n.applications.applications {
		app.pendingRequestsMu.RLock()
		responseChan, exists := app.pendingRequests[respFrame.CorrelationID]
		app.pendingRequestsMu.RUnlock()

		if exists {
			select {
			case responseChan <- &respFrame:
			default:
				n.logger.Warn("response channel full", "correlationID", respFrame.CorrelationID)
			}
			return
		}
	}

	n.logger.Warn("received response for unknown request", "correlationID", respFrame.CorrelationID)
}

func (n *Nodosum) streamReadLoop(stream *quic.Stream, nodeID, appID string) {
	defer func() {
		stream.Close()
		n.logger.Debug("stream read loop closed", "nodeId", nodeID, "appId", appID)
	}()

	for {
		select {
		case <-n.ctx.Done():
			n.logger.Debug("quic stream read loop closed")
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
				continue
			}

			// Handle different frame types
			switch frameType {
			case FRAME_TYPE_DATA:
				n.routeToApplication(appID, payload, nodeID)
			case FRAME_TYPE_REQUEST:
				n.handleRequest(stream, appID, payload, nodeID)
			case FRAME_TYPE_RESPONSE:
				n.handleResponse(payload)
			default:
				n.logger.Warn("unknown frame type", "type", frameType)
			}
		}
	}
}

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
