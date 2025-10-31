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

// TODO: Introduce UDP for connection negotiation

func (n *Nodosum) listenUdp() {
	n.wg.Go(
		func() {
			<-n.ctx.Done()
			err := n.udpConn.Close()
			if err != nil {
				n.logger.Info("udpConn close failed", "error", err.Error())
			}
			n.logger.Info("listenerTcp closed")
		},
	)

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
			buf := make([]byte, 1024)
			bytesRead, addr, err := n.udpConn.ReadFromUDP(buf)
			if err != nil {
				n.logger.Info("udp read failed", "error", err.Error(), "bytesRead", bytesRead, "addr", addr)
			}

			go n.handleUdp(buf[:bytesRead])
		}
	}

}

func (n *Nodosum) handleUdp(bytes []byte) {}

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

func (n *Nodosum) startRwLoops(id uint32) {
	defer n.wg.Done()
	n.wg.Add(2)

	go n.writeLoop(id)
	go n.readLoop(id)
}

func (n *Nodosum) serverHandshake(conn net.Conn) uint32 {
	return 0
}

func (n *Nodosum) readLoop(id uint32) {
	defer n.wg.Done()

	v, ok := n.connections.Load(id)
	if !ok {
		return
	}
	connChan := v.(*nodeConn)

	for {
		select {
		case <-connChan.ctx.Done():
			n.logger.Debug(fmt.Sprintf("read loop for %d cancelled", id))
			return
		default:
			// Receive and decode frame header
			headerBytes := make([]byte, 11)
			i, err := io.ReadFull(connChan.conn, headerBytes)
			if err != nil {
				n.handleConnError(err, id)
				continue
			}
			if i != 11 {
				n.handleConnError(fmt.Errorf("invalid frame header length %d", i), id)
				continue
			}

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

func (n *Nodosum) writeLoop(id uint32) {
	defer n.wg.Done()

	v, ok := n.connections.Load(id)
	if !ok {
		return
	}
	connChan := v.(*nodeConn)

	for {
		select {
		case <-connChan.ctx.Done():
			n.logger.Debug(fmt.Sprintf("write loop for %d cancelled", id))
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

func (n *Nodosum) handleConnError(err error, chanId uint32) {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.ErrUnexpectedEOF) {
		n.logger.Debug("closing conn because of closed connection or deadline exceeded")
		n.closeConnChannel(chanId)
	}
	if err != nil {
		n.logger.Error("error reading from tcp connection", "error", err.Error())
	}
}
