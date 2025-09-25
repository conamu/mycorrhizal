package nodosum

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/conamu/mycorrizal/internal/packet"
)

/*

SCOPE

- Discover Instances via Consul API/DNS-SD
- Establish Connections in Star Network Topology (all nodes have a connection to all nodes)
- Manage connections and keep them up
- Provide communication interface to abstract away the cluster
  (this should feel like one big App, even though it could be spread on 10 nodes/instances)
- Provide Interface to create, read, update and delete cluster store resources
  and find/set their location on the cluster.
- Authenticate and Encrypt all Intra-Cluster Communication

*/

type Nodosum struct {
	nodeId           string
	ctx              context.Context
	listener         net.Listener
	registry         *nodeRegistry
	logger           *slog.Logger
	channelRegistry  *sync.Map
	wg               *sync.WaitGroup
	handshakeTimeout time.Duration
	tlsEnabled       bool
	tlsConfig        *tls.Config
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

func New(cfg *Config) (*Nodosum, error) {
	var tlsConf *tls.Config

	lAddr := &net.TCPAddr{Port: cfg.ListenPort}
	addrString := lAddr.String()

	if cfg.TlsEnabled {
		cfg.Logger.Debug("running with TLS enabled")
		tlsConf = &tls.Config{
			ServerName:   "localhost",
			RootCAs:      cfg.TlsCACert,
			Certificates: []tls.Certificate{*cfg.TlsCert},
		}
	}
	listener, err := net.Listen("tcp", addrString)
	if err != nil {
		return nil, err
	}
	registry := newNodeRegistry()

	return &Nodosum{
		nodeId:           cfg.NodeId,
		ctx:              cfg.Ctx,
		listener:         listener,
		registry:         registry,
		logger:           cfg.Logger,
		channelRegistry:  &sync.Map{},
		wg:               cfg.Wg,
		handshakeTimeout: cfg.HandshakeTimeout,
		tlsEnabled:       cfg.TlsEnabled,
		tlsConfig:        tlsConf,
	}, nil
}

func (n *Nodosum) Start() {
	n.wg.Go(
		func() {
			err := n.listen()
			if err != nil {
				n.logger.Error("nodosum error while starting", "error", err.Error())
			}
		},
	)
}

func (n *Nodosum) Shutdown() {
	n.channelRegistry.Range(func(k, v interface{}) bool {
		id := k.(string)
		n.closeConnChannel(id)
		return true
	})
}

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
				tlsConn := tls.Server(conn, n.tlsConfig)
				hsCtx, _ := context.WithDeadline(n.ctx, time.Now().Add(n.handshakeTimeout))
				err = tlsConn.HandshakeContext(hsCtx)
				if err != nil {
					n.logger.Warn("error setting handshake context", "error", err.Error())
				}
				err = tlsConn.Handshake()
				if err != nil {
					n.logger.Error("error handshake TLS connection", "error", err.Error(), "remote", conn.RemoteAddr())
					conn.Close()
					return nil
				}
				conn = tlsConn
			}
			n.wg.Add(1)
			go n.handleConn(conn)
		}
	}
}

func (n *Nodosum) handleConn(tcpConn net.Conn) {
	defer n.wg.Done()

	p, err := packet.Pack("HELLO", []byte(n.nodeId))
	if err != nil {
		// Return here since unsuccessful HELLO packet won´t get us a connection any ways
		n.logger.Error("error packing packet", "error", err.Error())
		return
	}

	numWrittenBytes, err := tcpConn.Write(p)
	if err != nil {
		n.logger.Error("error sending node id to tcp connection", err.Error())
	}
	if numWrittenBytes != len(p) {
		n.logger.Warn("packet contains more bytes than written to connection")
	}
	buff := make([]byte, 40690000)
	// Set a deadline in which a client needs to answer, else cut the connection assuming he don´t speak our language
	err = tcpConn.SetReadDeadline(time.Now().Add(n.handshakeTimeout))
	if err != nil {
		n.logger.Error("error setting read deadline", err.Error())
	}
	numReadBytes, err := tcpConn.Read(buff)
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		tcpConn.Close()
		return
	}
	if err != nil {
		n.logger.Error("error reading node id from tcp connection", "error", err.Error())
	}

	cmd, data, err := packet.Unpack(buff[:numReadBytes])
	if err != nil {
		tcpConn.Close()
		n.logger.Error("error unpacking glutamate packet", "error", err.Error(), "nBytes", numReadBytes, "remote", tcpConn.RemoteAddr())
		return
	}

	fmt.Println(cmd, string(data))

	nodeConnId := string(data)

	err = tcpConn.SetReadDeadline(time.Time{})
	if err != nil {
		n.logger.Error("error setting read deadline", err.Error())
	}

	n.createNewChannel(nodeConnId, tcpConn)
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
					command, data, err := packet.Unpack(msg)
					if err != nil {
						n.logger.Error("error unpacking glutamate packet", "error", err.Error())
					}
					log.Println(command)
					log.Println(string(data))
					if command == "ID" {
						p, err := packet.Pack("ID", []byte(n.nodeId))
						if err != nil {
							n.logger.Error("error packing glutamate packet", "error", err.Error())
						}
						connChan.writeChan <- p
					}
					if command == "EXIT" {
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
						log.Println("error writing to tcp connection", "error", err.Error())
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
					buff := make([]byte, 40960000)
					i, err := connChan.conn.Read(buff)
					if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) {
						n.logger.Error("error reading from tcp connection", "error", err.Error())
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
