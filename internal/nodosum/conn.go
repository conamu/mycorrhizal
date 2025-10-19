package nodosum

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"time"
)

// TODO: Introduce UDP for connection negotiation

func (n *Nodosum) listen() error {
	n.wg.Go(
		func() {
			<-n.ctx.Done()
			err := n.listener.Close()
			if err != nil {
				n.logger.Info("listener close failed", "error", err.Error())
			}
			n.logger.Info("listener closed")
		},
	)

	// Listener Accept Loop
	for {
		select {
		case <-n.ctx.Done():
			return nil
		default:
			conn, err := n.listener.Accept()
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

func (n *Nodosum) startRwLoops(id string) {
	defer n.wg.Done()
	n.wg.Add(2)

	// Write Loop
	go n.writeLoop(id)
	// Read Loop
	go n.readLoop(id)
}

func (n *Nodosum) serverHandshake(conn net.Conn) string {
	p, err := pack("HANDSHAKE", HELLO, []byte(n.nodeId), "")
	if err != nil {
		// Return here since unsuccessful HELLO packet won´t get us a connection any ways
		n.logger.Error("error packing packet", "error", err.Error())
		return ""
	}

	numWrittenBytes, err := conn.Write(p)
	if err != nil {
		n.logger.Error("error sending node id to tcp connection", err.Error())
	}
	if numWrittenBytes != len(p) {
		n.logger.Warn("packet contains more bytes than written to connection")
	}
	buff := make([]byte, 40690000)
	// Set a deadline in which a client needs to answer, else cut the connection assuming he don´t speak our language
	err = conn.SetReadDeadline(time.Now().Add(n.handshakeTimeout))
	if err != nil {
		n.logger.Error("error setting read deadline", err.Error())
	}
	numReadBytes, err := conn.Read(buff)
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		conn.Close()
		return ""
	}
	if err != nil {
		n.logger.Error("error reading node id from tcp connection", "error", err.Error())
	}

	pack, err := unpack(buff[:numReadBytes])
	if err != nil {
		conn.Close()
		n.logger.Error("error unpacking glutamate packet", "error", err.Error(), "nBytes", numReadBytes, "remote", conn.RemoteAddr())
		return ""
	}

	if pack.Command != HELLO {
		conn.Close()
		n.logger.Warn("client did not answer correctly with HELLO", "remote", conn.RemoteAddr())
	}
	return string(pack.Data)
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
			n.logger.Debug("read loop for " + id + " cancelled")
			return
		default:
			buff := make([]byte, 4096)
			i, err := connChan.conn.Read(buff)
			if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) {
				n.logger.Debug("closing conn because of closed connection or deadline exceeded")
				n.closeConnChannel(id)
				continue
			}
			if err != nil {
				n.logger.Error("error reading from tcp connection", "error", err.Error())
				continue
			}
			msg := string(buff[:i])
			connChan.readChan <- []byte(msg)
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
			n.logger.Debug("write loop for " + id + " cancelled")
			return
		case msg := <-connChan.writeChan:
			if msg == nil {
				continue
			}
			_, err := connChan.conn.Write(msg.([]byte))
			if err != nil {
				n.logger.Error("error writing to tcp connection", "error", err.Error())
			}
		}
	}
}
