package nodosum

import (
	"fmt"
	"time"

	"github.com/conamu/go-worker"
)

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
	nodosum       *Nodosum
	receiveFunc   func(payload []byte) error
	nodes         []string
	receiveWorker *worker.Worker
}

type dataPackage struct {
	id             string
	payload        []byte
	fromNodeId     string
	receivingNodes []string
}

func (n *Nodosum) RegisterApplication(uniqueIdentifier string) Application {
	receiveWorker := worker.NewWorker(n.ctx, fmt.Sprintf("%s-receive", uniqueIdentifier), n.wg, n.applicationReceiveTask, n.logger, 0)
	receiveWorker.InputChan = make(chan any, 100)
	go receiveWorker.Start()

	nodes := []string{}

	n.nodeMeta.Lock()
	for _, meta := range n.nodeMeta.Map {
		if !meta.alive {
			continue
		}
		nodes = append(nodes, meta.ID)
	}
	n.nodeMeta.Unlock()

	app := &application{
		id:            uniqueIdentifier,
		nodosum:       n,
		receiveWorker: receiveWorker,
		nodes:         nodes,
	}

	n.applications.Lock()
	n.applications.applications[uniqueIdentifier] = app
	n.applications.Unlock()
	return app
}

func (n *Nodosum) GetApplication(uniqueIdentifier string) Application {
	n.applications.RLock()
	app, exists := n.applications.applications[uniqueIdentifier]
	n.applications.RUnlock()
	if exists {
		return app
	}
	return nil
}

func (a *application) Send(payload []byte, ids []string) error {
	// Determine target nodes
	targetNodes := ids
	if len(targetNodes) == 0 {
		// Broadcast: get all connected nodes
		targetNodes = a.nodosum.getConnectedNodes()
	}

	if len(targetNodes) == 0 {
		return fmt.Errorf("no connected nodes to send to")
	}

	// Send to each target node via dedicated QUIC stream
	var sendErrors []error
	for _, nodeId := range targetNodes {
		// Get or create stream for this app to this node
		stream, err := a.nodosum.getOrOpenQuicStream(nodeId, a.id, "data")
		if err != nil {
			a.nodosum.logger.Error("failed to get stream", "error", err, "nodeId", nodeId, "app", a.id)
			sendErrors = append(sendErrors, fmt.Errorf("node %s: %w", nodeId, err))
			continue
		}

		// Encode and write data frame to stream
		frame := encodeDataFrame(payload)
		_, err = (*stream).Write(frame)
		if err != nil {
			a.nodosum.logger.Error("failed to write to stream", "error", err, "nodeId", nodeId, "app", a.id)
			sendErrors = append(sendErrors, fmt.Errorf("node %s: %w", nodeId, err))
			continue
		}
	}

	if len(sendErrors) > 0 {
		return fmt.Errorf("failed to send to %d/%d nodes: %v", len(sendErrors), len(targetNodes), sendErrors)
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
	return a.nodosum.getConnectedNodes()
}

func (n *Nodosum) applicationReceiveTask(w *worker.Worker, msg any) {
	w.Logger.Warn("application receive callback is not set")
}

func (n *Nodosum) routeToApplication(appID string, payload []byte, fromNode string) {
	n.applications.RLock()
	app, exists := n.applications.applications[appID]
	n.applications.RUnlock()

	if !exists {
		n.logger.Warn("received message for unknown application", "appID", appID)
		return
	}

	// Send to application's receive worker
	select {
	case app.receiveWorker.InputChan <- dataPackage{
		payload:    payload,
		fromNodeId: fromNode,
	}:
	case <-time.After(100 * time.Millisecond):
		n.logger.Warn("application receive channel full, message discarded", "appID", appID)
	}
}
