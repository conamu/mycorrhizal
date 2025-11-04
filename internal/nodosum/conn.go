package nodosum

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func (n *Nodosum) listenUdp() {
	n.wg.Go(
		func() {
			<-n.ctx.Done()
			err := n.udpConn.Close()
			if err != nil {
				n.logger.Info("udpConn close failed", "error", err.Error())
			}
			n.logger.Info("listenerUdp closed")
		},
	)

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
			buf := make([]byte, 1024)
			bytesRead, addr, err := n.udpConn.ReadFromUDP(buf)
			go n.handleUdp(buf, bytesRead, addr, err)
		}
	}

}

// handleUdp will decode the packet received and based on the defined udp handshake
// it will enable creation of a permanent tcp connection
func (n *Nodosum) handleUdp(buf []byte, bytesRead int, addr *net.UDPAddr, err error) {

	err = n.handleUdpError(err)
	if err != nil {
		n.logger.Error("udp read failed", "error", err.Error(), "bytesRead", bytesRead, "addr", addr)
	}

	hp := n.decodeHandshakePacket(buf)

	if hp == nil {
		return
	}

	if hp.Version == 0 {
		n.logger.Debug("version is 0")
		return
	}

	if hp.Secret != n.sharedSecret {
		n.logger.Warn("tried to initiate udp handshake with invalid secret")
		return
	}

	responsePacket := n.handleUdpPacket(hp, addr.String())

	var responsePacketBytes []byte

	if responsePacket != nil {
		responsePacketBytes = n.encodeHandshakePacket(responsePacket)
	}

	i, err := n.udpConn.WriteToUDP(responsePacketBytes, addr)
	if err != nil {
		n.logger.Info("write udp response failed", "error", err.Error())
	}
	if i != len(responsePacketBytes) {
		n.logger.Warn("write udp response byte count not matching")
	}
}

func (n *Nodosum) listenTcp() {
	n.wg.Go(
		func() {
			<-n.ctx.Done()
			err := n.listenerTcp.Close()
			if err != nil {
				n.logger.Info("listenerTcp close failed", "error", err.Error())
			}
			n.logger.Info("listenerTcp closed")
		},
	)

	// Listener Accept Loop
	for {
		select {
		case <-n.ctx.Done():
			return
		default:
			conn, err := n.listenerTcp.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					n.logger.Error("error accepting TCP connection", err.Error())
				}
				continue
			}
			if n.tlsEnabled {
				conn = n.upgradeConn(conn)
				if conn == nil {
					continue
				}
			}
			n.wg.Add(1)
			go n.handleConn(conn)
		}
	}
}

func (n *Nodosum) upgradeConn(conn net.Conn) net.Conn {
	tlsConn := tls.Server(conn, n.tlsConfig)
	hsCtx, _ := context.WithDeadline(n.ctx, time.Now().Add(n.handshakeTimeout))
	err := tlsConn.HandshakeContext(hsCtx)
	if err != nil {
		n.logger.Warn("error setting handshake context", "error", err.Error())
	}
	err = tlsConn.Handshake()
	if err != nil {
		n.logger.Error("error handshake TLS connection", "error", err.Error(), "remote", conn.RemoteAddr())
		conn.Close()
		return nil
	}
	return tlsConn
}

func (n *Nodosum) handleConn(conn net.Conn) {
	defer n.wg.Done()

	nodeConnId := n.serverHandshake(conn)

	err := conn.SetReadDeadline(time.Time{})
	if err != nil {
		n.logger.Error("error setting read deadline", err.Error())
	}

	n.createConnChannel(nodeConnId, conn)
	n.wg.Add(1)
	go n.startRwLoops(nodeConnId)
}

// serverHandshake will be called to redeem a one-time connection token
// that the server received from the udp based handshake
func (n *Nodosum) serverHandshake(conn net.Conn) string {

	connInitFrameHeader := encodeFrameHeader(&frameHeader{
		Version:       1,
		ApplicationID: n.nodeId,
		Type:          SYSTEM,
		Flag:          CONN_INIT,
		Length:        0,
	})

	i, err := conn.Write(connInitFrameHeader)
	if err != nil {
		n.logger.Error("error sending conn init frame header", "error", err.Error())
	}
	if i != len(connInitFrameHeader) {
		n.logger.Warn("error sending conn init frame header", "error", fmt.Sprintf("expected: %d, actual: %d", i, len(connInitFrameHeader)))
	}

	err = conn.SetReadDeadline(time.Now().Add(n.handshakeTimeout))
	if err != nil {
		n.logger.Error("error setting read deadline", err.Error())
	}

	readBuf := make([]byte, 1024)

	i, err = conn.Read(readBuf)
	if err != nil {
		n.logger.Error("error reading conn init frame header", "error", err.Error())
	}

	err = conn.SetReadDeadline(time.Time{})
	if err != nil {
		n.logger.Error("error setting read deadline", err.Error())
	}

	fh := decodeFrameHeader(readBuf[:i])

	readBuf = make([]byte, fh.Length)
	i, err = conn.Read(readBuf)
	if err != nil {
		n.logger.Error("error reading conn init token frame header", "error", err.Error())
	}

	token := n.connInit.tokens[fh.ApplicationID]

	if token != string(readBuf) {
		n.logger.Warn("tcp handshake secret token challenge not accepted", "token", string(readBuf))
		return ""
	}
	// Remove the token once used
	delete(n.connInit.tokens, fh.ApplicationID)

	return fh.ApplicationID
}

func (n *Nodosum) startRwLoops(id string) {
	n.wg.Add(2)

	n.logger.Debug("startRwLoops", "id", id)

	go n.writeLoop(id)
	go n.readLoop(id)
}

func (n *Nodosum) readLoop(id string) {
	defer n.wg.Done()

	v, ok := n.connections.Load(id)
	if !ok {
		return
	}
	connChan := v.(*nodeConn)

	for {
		select {
		case <-connChan.ctx.Done():
			n.logger.Debug(fmt.Sprintf("read loop for %s cancelled", id))
			return
		default:
			// Read fixed header (9 bytes: version, type, flag, length, appIDLen)
			fixedHeaderBytes := make([]byte, 9)
			i, err := io.ReadFull(connChan.conn, fixedHeaderBytes)
			if err != nil {
				n.handleConnError(err, id)
				continue
			}
			if i != 9 {
				n.handleConnError(fmt.Errorf("invalid fixed header length %d", i), id)
				continue
			}

			// Read ApplicationID length from header
			appIDLen := int(fixedHeaderBytes[7]) | int(fixedHeaderBytes[8])<<8

			// Read variable ApplicationID
			appIDBytes := make([]byte, appIDLen)
			i, err = io.ReadFull(connChan.conn, appIDBytes)
			if err != nil {
				n.handleConnError(err, id)
				continue
			}
			if i != appIDLen {
				n.handleConnError(fmt.Errorf("invalid appID length %d", i), id)
				continue
			}

			// Combine fixed header + appID for decoding
			headerBytes := append(fixedHeaderBytes, appIDBytes...)
			header := decodeFrameHeader(headerBytes)
			payloadLength := header.Length
			payloadBytes := make([]byte, payloadLength)

			// Read Payload
			i, err = io.ReadFull(connChan.conn, payloadBytes)
			if err != nil {
				n.handleConnError(err, id)
				continue
			}
			if i != int(header.Length) {
				n.handleConnError(fmt.Errorf("invalid frame payload length %d", i), id)
				continue
			}

			frameBytes := append(headerBytes, payloadBytes...)

			connChan.readChan <- frameBytes
		}
	}
}

func (n *Nodosum) writeLoop(id string) {
	defer n.wg.Done()

	v, ok := n.connections.Load(id)
	if !ok {
		return
	}
	connChan := v.(*nodeConn)

	for {
		select {
		case <-connChan.ctx.Done():
			n.logger.Debug(fmt.Sprintf("write loop for %s cancelled", id))
			return
		case msg := <-connChan.writeChan:
			if msg == nil {
				continue
			}

			_, err := connChan.conn.Write(append(msg.([]byte)))
			if err != nil {
				n.logger.Error("error writing to tcp connection", "error", err.Error())
			}
		}
	}
}

func (n *Nodosum) handleConnError(err error, chanId string) {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.ErrUnexpectedEOF) {
		n.logger.Debug("closing conn because of closed connection or deadline exceeded")
		n.closeConnChannel(chanId)
	}
	if err != nil {
		n.logger.Error("error reading from tcp connection", "error", err.Error())
	}
}

func (n *Nodosum) handleUdpError(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}
