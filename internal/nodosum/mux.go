package nodosum

import "github.com/conamu/go-worker"

/*
Connection Multiplexing and application traffic filtering

Instance has 1 channel for everything incoming.
Instance has N channels for outgoing connection to N instances.

read incoming packets:
Packets/Commands have an application identifier.
If an application is actively registered through nodosum,
the command gets routed to its read channel.

*/

func (n *Nodosum) StartMultiplexer() {

	for range n.muxWorkerCount {
		mpWorker := worker.NewWorker(n.ctx, "multiplexer-inbound", n.wg, n.multiplexerTaskInbound, n.logger, 0)
		mpWorker.InputChan = n.globalReadChannel
		go mpWorker.Start()
	}

	for range n.muxWorkerCount {
		mpWorker := worker.NewWorker(n.ctx, "multiplexer-outbound", n.wg, n.multiplexerTaskOutbound, n.logger, 0)
		mpWorker.InputChan = n.globalWriteChannel
		go mpWorker.Start()
	}

}

// multiplexerTaskInbound processes all packets coming from individual connections
func (n *Nodosum) multiplexerTaskInbound(w *worker.Worker, msg any) {
	frame := msg.([]byte)
	header := decodeFrameHeader(frame[0:11])
	val, ok := n.applications.Load(header.ApplicationID)
	if ok && val != nil {
		app := val.(application)
		// Only send payload to application
		app.receiveWorker.InputChan <- frame[11:]
	}
}

// multiplexerTaskInbound processes all packets from applications, routing them to specified individual connections
func (n *Nodosum) multiplexerTaskOutbound(w *worker.Worker, msg any) {
	dataPack := msg.(*dataPackage)

	fh := frameHeader{
		Version:       0,
		ApplicationID: dataPack.id,
		Type:          APP,
		Length:        uint32(len(dataPack.payload)),
	}

	header := encodeFrameHeader(&fh)
	frame := append(header, dataPack.payload...)

	for _, id := range dataPack.receivingNodes {
		val, ok := n.connections.Load(id)
		if ok && val != nil {
			nc := val.(*nodeConn)
			nc.writeChan <- frame
		}
	}

}
