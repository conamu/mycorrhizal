package nodosum

import (
	"errors"
	"fmt"

	"github.com/conamu/go-worker"
)

/*
Introduce a configurable amount of workers that do this:
one type of worker should receive all packets of a connection,
unpack and send to the different subsystems that should receive them by ID

other type of worker gets all requests, generates IDs,
adds IDs to some service registry and sends all packets to the corresponding connections.

Could also think of introducing Subsystem or Application IDs as channels.
Sort of like messaging topics, multiplexed ontop of a TCP connection between 2 nodes.
This could work well, also as a basis for the other systems.

Concept of Applications:
Applications can register themselves with a unique string identifier that has to be unique among the whole
cluster. An application gets its own "channel".
Packets for one application are always Identified by its Identifier and routed to this one application.
*/

type Application interface {
	// Send sends a Command to one or more Nodes specified by ID. Specifying no ID will broadcast the packet to all Nodes.
	Send(payload []byte, ids []string) error
	// SetReceiveFunc registers a function that is executed to handle the Command received.
	SetReceiveFunc(func(payload []byte) error)
	// Nodes retrieves the ID info about nodes in the cluster to enable the application to work with the clusters resources.
	Nodes() []string
}

type application struct {
	id            string
	receiveFunc   func(payload []byte) error
	nodes         []string
	sendWorker    *worker.Worker
	receiveWorker *worker.Worker
}

type dataPackage struct {
	id             string
	payload        []byte
	receivingNodes []string
}

func (n *Nodosum) RegisterApplication(uniqueIdentifier string) Application {

	sendWorker := worker.NewWorker(n.ctx, fmt.Sprintf("%s-send", uniqueIdentifier), n.wg, n.applicationSendTask, n.logger, 0)
	sendWorker.InputChan = make(chan any, 100)
	sendWorker.OutputChan = n.globalWriteChannel
	go sendWorker.Start()

	receiveWorker := worker.NewWorker(n.ctx, fmt.Sprintf("%s-receive", uniqueIdentifier), n.wg, n.applicationReceiveTask, n.logger, 0)
	receiveWorker.InputChan = make(chan any, 100)
	go receiveWorker.Start()

	nodes := []string{}

	n.nodeMeta.Mu.Lock()
	for _, meta := range n.nodeMeta.Map {
		if !meta.alive {
			continue
		}
		nodes = append(nodes, meta.ID)
	}
	n.nodeMeta.Mu.Unlock()

	app := &application{
		id:            uniqueIdentifier,
		sendWorker:    sendWorker,
		receiveWorker: receiveWorker,
		nodes:         nodes,
	}

	n.applications.Store(uniqueIdentifier, app)

	//TODO: Find a way to keep nodes in sync for the application instances

	return app
}

func (n *Nodosum) GetApplication(uniqueIdentifier string) Application {
	val, ok := n.applications.Load(uniqueIdentifier)
	if ok {
		return val.(*application)
	}
	return nil
}

func (a *application) Send(payload []byte, ids []string) error {
	select {
	case <-a.sendWorker.Ctx.Done():
		return errors.New("send channel closed")
	default:
		dp := dataPackage{
			id:             a.id,
			payload:        payload,
			receivingNodes: ids,
		}

		a.sendWorker.InputChan <- &dp
	}

	return nil
}

func (a *application) SetReceiveFunc(callback func(payload []byte) error) {
	a.receiveWorker.TaskFunc = func(w *worker.Worker, msg any) {
		pl := msg.([]byte)
		err := callback(pl)
		if err != nil {
			w.Logger.Error("callback function error", "err", err.Error())
		}
	}
}

func (a *application) Nodes() []string {
	return a.nodes
}

func (n *Nodosum) applicationSendTask(w *worker.Worker, msg any) {
	w.OutputChan <- msg
}

func (n *Nodosum) applicationReceiveTask(w *worker.Worker, msg any) {
	w.Logger.Warn("application receive callback is not set")
}
