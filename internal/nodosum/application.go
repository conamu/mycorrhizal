package nodosum

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/conamu/go-worker"
	"github.com/google/uuid"
)

type Application interface {
	// Send sends a Command to one or more Nodes specified by ID. Specifying no ID will broadcast the packet to all Nodes.
	Send(payload []byte, ids []string) error
	// Request sends a request to a target node, expecting a response before the timeout
	Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error)
	// SetReceiveFunc registers a function that is executed to handle the Command received.
	SetReceiveFunc(func(payload []byte) error)
	// SetRequestHandler sets a handling function with simple bytes in bytes out logic for req/res communication
	SetRequestHandler(func(payload []byte, senderId string) (responsePayload []byte, err error))
	// Nodes retrieves the ID info about nodes in the cluster to enable the application to work with the clusters resources.
	Nodes() []string
}

type application struct {
	id                string
	nodosum           *Nodosum
	receiveFunc       func(payload []byte) error
	nodes             []string
	receiveWorker     *worker.Worker
	pendingRequests   map[string]chan *ResponseFrame
	pendingRequestsMu sync.RWMutex
	requestHandler    func([]byte, string) ([]byte, error)
	ctx               context.Context
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
		id:              uniqueIdentifier,
		nodosum:         n,
		receiveWorker:   receiveWorker,
		nodes:           nodes,
		pendingRequests: make(map[string]chan *ResponseFrame),
		ctx:             n.ctx,
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
		n, err := (*stream).Write(frame)
		if err != nil {
			a.nodosum.logger.Error("failed to write to stream", "error", err, "nodeId", nodeId, "app", a.id)
			sendErrors = append(sendErrors, fmt.Errorf("node %s: %w", nodeId, err))
			continue
		}
		a.nodosum.logger.Debug("sent data frame", "nodeId", nodeId, "app", a.id, "payloadSize", len(payload), "frameSize", n)
	}

	if len(sendErrors) > 0 {
		return fmt.Errorf("failed to send to %d/%d nodes: %v", len(sendErrors), len(targetNodes), sendErrors)
	}

	return nil
}

func (a *application) Request(payload []byte, targetNode string, timeout time.Duration) ([]byte, error) {
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	// Generate correlation ID
	correlationID := uuid.New().String()

	// Create response channel
	responseChan := make(chan *ResponseFrame, 1)
	a.pendingRequestsMu.Lock()
	a.pendingRequests[correlationID] = responseChan
	a.pendingRequestsMu.Unlock()

	// Cleanup on return
	defer func() {
		a.pendingRequestsMu.Lock()
		delete(a.pendingRequests, correlationID)
		a.pendingRequestsMu.Unlock()
	}()

	// Send request frame
	reqFrame := RequestFrame{
		CorrelationID: correlationID,
		Payload:       payload,
	}
	err := a.sendRequestFrame(reqFrame, targetNode)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-responseChan:
		if resp.Error != "" {
			return nil, fmt.Errorf("remote error: %s", resp.Error)
		}
		return resp.Payload, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("request timeout after %v", timeout)
	case <-a.ctx.Done():
		return nil, fmt.Errorf("context cancelled")
	}
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

func (a *application) SetRequestHandler(handler func([]byte, string) ([]byte, error)) {
	a.requestHandler = handler
}

func (a *application) Nodes() []string {
	return a.nodosum.getConnectedNodes()
}

// sendRequestFrame sends a request frame to a target node using a dedicated stream.
// This is used internally by the Request method to send request/response pattern messages.
func (a *application) sendRequestFrame(reqFrame RequestFrame, targetNode string) error {
	// Get or create stream for this app to target node
	// Use "request" stream name to differentiate from "data" streams
	stream, err := a.nodosum.getOrOpenQuicStream(targetNode, a.id, "request")
	if err != nil {
		return fmt.Errorf("failed to get stream: %w", err)
	}

	// Encode and write request frame to stream
	frame := encodeRequestFrame(reqFrame)
	_, err = (*stream).Write(frame)
	if err != nil {
		return fmt.Errorf("failed to write to stream: %w", err)
	}

	return nil
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

	timeoutChan := time.After(100 * time.Millisecond)

	// Send to application's receive worker (just the payload, not wrapped in struct)
	for {
		select {
		case app.receiveWorker.InputChan <- payload:
			n.logger.Debug("received message for application", "appID", appID)
			return
		case <-timeoutChan:
			n.logger.Warn("application receive channel full, message discarded",
				"appID", appID, "fromNode", fromNode)
			return
		}
	}

}
