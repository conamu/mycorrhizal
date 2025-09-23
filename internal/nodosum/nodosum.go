package nodosum

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"sync"
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
	nodeId          string
	ctx             context.Context
	listener        *net.TCPListener
	registry        *nodeRegistry
	logger          *slog.Logger
	channelRegistry nodeConnChannelsRegistry
	wg              *sync.WaitGroup
}

type nodeConnChannelsRegistry map[string]nodeConnChannel
type nodeConnChannel struct {
	connId    string
	conn      *net.TCPConn
	readChan  chan []byte
	writeChan chan []byte
}

func newNodeConnChannelsRegistry() nodeConnChannelsRegistry {
	return make(map[string]nodeConnChannel)
}

func (nccr nodeConnChannelsRegistry) createNewChannel(id string, conn *net.TCPConn) {
	nccr[id] = nodeConnChannel{
		connId:    id,
		conn:      conn,
		readChan:  make(chan []byte),
		writeChan: make(chan []byte),
	}
}

func (nccr nodeConnChannelsRegistry) deleteConnChannel(id string) {
	nccr[id].conn.Close()
	close(nccr[id].readChan)
	close(nccr[id].writeChan)
	delete(nccr, id)
}

func New(cfg *Config) (*Nodosum, error) {
	var tcpListener = new(net.TCPListener)

	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: cfg.ListenPort})
	if err != nil {
		return nil, err
	}

	registry := newNodeRegistry()

	return &Nodosum{
		ctx:             cfg.Ctx,
		listener:        tcpListener,
		registry:        registry,
		logger:          cfg.Logger,
		channelRegistry: newNodeConnChannelsRegistry(),
		wg:              cfg.Wg,
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
			tcpConn, err := n.listener.AcceptTCP()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					n.logger.Error("error accepting TCP connection", err.Error())
				}
				continue
			}
			n.wg.Add(1)
			go n.handleConn(tcpConn)
		}
	}
}

func (n *Nodosum) handleConn(tcpConn *net.TCPConn) {
	defer n.wg.Done()
	_, err := tcpConn.Write([]byte(n.nodeId))
	if err != nil {
		n.logger.Error("error sending node id to tcp connection", err.Error())
	}
	buff := make([]byte, 1024)
	i, err := tcpConn.Read(buff)
	if err != nil {
		n.logger.Error("error reading node id from tcp connection", err.Error())
	}
	nodeConnId := string(buff[:i])

	n.channelRegistry.createNewChannel(nodeConnId, tcpConn)
	go n.rwLoop(nodeConnId)
	n.wg.Go(
		func() {
			for {
				if _, exists := n.channelRegistry["debug-id\r\n"]; exists {
					break
				}
			}

			for {
				select {
				case <-n.ctx.Done():
					return
				case msg := <-n.channelRegistry[nodeConnId].readChan:
					log.Println(string(msg))
				}
			}
		},
	)
}

func (n *Nodosum) rwLoop(id string) {
	n.wg.Go(
		func() {
			for {
				select {
				case <-n.ctx.Done():
					return
				case msg := <-n.channelRegistry[id].writeChan:
					if msg == nil {
						return
					}
					_, err := n.channelRegistry[id].conn.Write(msg)
					if err != nil {
						log.Println("error writing to tcp connection", "error", err.Error())
					}
				}
			}
		},
	)

	n.wg.Go(
		func() {
			for {
				select {
				case <-n.ctx.Done():
					return
				default:
					buff := make([]byte, 1024)
					i, err := n.channelRegistry[id].conn.Read(buff)
					if errors.Is(err, io.EOF) {
						n.channelRegistry.deleteConnChannel(id)
						return
					}
					if err != nil {
						log.Println("error reading from tcp connection", err.Error())
					}
					n.channelRegistry[id].readChan <- buff[:i]
				}
			}
		},
	)
}
