package nodosum

import (
	"github.com/conamu/go-worker"
)

/* TODO:
Extract worker library as a module (finally)
add it here
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
	Send(cmd Command, ids []string) error
	// SetReceiveFunc registers a function that is executed to handle the Command received.
	SetReceiveFunc(func(cmd Command) error)
	// Nodes retrieves the ID info about nodes in the cluster to enable the application to work with the clusters resources.
	Nodes() []string
	Deregister()
}

type application struct {
	id            string
	receiveFunc   func(cmd Command) error
	nodes         []string
	sendWorker    *worker.Worker
	receiveWorker *worker.Worker
}

func (n *Nodosum) RegisterApplication(uniqueIdentifier string) Application {
	sendWorker := worker.NewWorker(n.ctx, uniqueIdentifier+"send", n.wg, n.applicationSendTask, n.logger, 0)
	sendWorker.Start()

	receiveWorker := worker.NewWorker(n.ctx, uniqueIdentifier+"receive", n.wg, n.applicationReceiveTask, n.logger, 0)
	receiveWorker.Start()

	//TODO: Find a way to keep nodes in sync for the application instances

	return &application{
		id:            uniqueIdentifier,
		sendWorker:    sendWorker,
		receiveWorker: receiveWorker,
	}
}

func (a *application) Send(cmd Command, ids []string) error {
	//TODO implement me
	panic("implement me")
}

func (a *application) SetReceiveFunc(f func(cmd Command) error) {
	a.receiveFunc = f
}

func (a *application) Nodes() []string {
	return a.nodes
}

func (a *application) Deregister() {
	// TODO Broadcast deregister message on demand
	a.receiveWorker.Stop()
	a.sendWorker.Stop()
}

func (n *Nodosum) applicationSendTask(w *worker.Worker, msg any) {
}

func (n *Nodosum) applicationReceiveTask(w *worker.Worker, msg any) {

}
