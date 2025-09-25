package nodosum

import (
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/conamu/mycorrizal/internal/packet"
)

func (n *Nodosum) handleConn(conn net.Conn) {
	defer n.wg.Done()

	p, err := packet.Pack(HELLO, []byte(n.nodeId), "")
	if err != nil {
		// Return here since unsuccessful HELLO packet won´t get us a connection any ways
		n.logger.Error("error packing packet", "error", err.Error())
		return
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
		return
	}
	if err != nil {
		n.logger.Error("error reading node id from tcp connection", "error", err.Error())
	}

	pack, err := packet.Unpack(buff[:numReadBytes])
	if err != nil {
		conn.Close()
		n.logger.Error("error unpacking glutamate packet", "error", err.Error(), "nBytes", numReadBytes, "remote", conn.RemoteAddr())
		return
	}

	if pack.Command != HELLO {
		conn.Close()
		n.logger.Warn("client did not answer correctly with HELLO", "remote", conn.RemoteAddr())
	}

	nodeConnId := string(pack.Data)

	err = conn.SetReadDeadline(time.Time{})
	if err != nil {
		n.logger.Error("error setting read deadline", err.Error())
	}

	n.createNewChannel(nodeConnId, conn)
	n.wg.Add(1)
	go n.rwLoop(nodeConnId)
	n.wg.Go(
		func() {
			v, ok := n.channelRegistry.Load(nodeConnId)
			if !ok {
				return
			}
			connChan := v.(*nodeConnChannel)

			for {
				select {
				case <-connChan.ctx.Done():
					return
				case msg := <-connChan.readChan:
					pack, err := packet.Unpack(msg)
					if err != nil {
						n.logger.Error("error unpacking glutamate packet", "error", err.Error())
					}
					n.logger.Debug("received command", "command", pack.Command)

					if !middleware(pack.Command, pack.Token) {
						p, err := packet.Pack(DENY, []byte("not authorized for this command"), pack.Token)
						n.logger.Warn("unauthorized command", "command", pack.Command, "token", pack.Token)
						if err != nil {
							n.logger.Error("error packing glutamate packet", "error", err.Error())
						}
						connChan.writeChan <- p
						continue
					}

					handler := n.handler(pack, connChan.writeChan)
					shouldReturn, err := handler()
					if err != nil {
						n.logger.Error("error handling glutamate command packet", "error", err.Error())
					}

					if shouldReturn {
						n.closeConnChannel(nodeConnId)
						return
					}
				}
			}
		},
	)
}

func (n *Nodosum) rwLoop(id string) {
	defer n.wg.Done()

	// Write Loop
	n.wg.Go(
		func() {
			v, ok := n.channelRegistry.Load(id)
			if !ok {
				return
			}
			connChan := v.(*nodeConnChannel)

			for {
				select {
				case <-connChan.ctx.Done():
					n.logger.Debug("write loop for " + id + " cancelled")
					return
				case msg := <-connChan.writeChan:
					if msg == nil {
						continue
					}
					_, err := connChan.conn.Write(msg)
					if err != nil {
						n.logger.Error("error writing to tcp connection", "error", err.Error())
					}
				}
			}
		},
	)

	// Read Loop
	n.wg.Go(
		func() {
			v, ok := n.channelRegistry.Load(id)
			if !ok {
				return
			}
			connChan := v.(*nodeConnChannel)

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
		},
	)
}
