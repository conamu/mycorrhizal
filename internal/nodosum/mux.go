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
	// Read appIDLen from bytes 7-8 to determine header size
	if len(frame) < 9 {
		n.logger.Error("frame too short for header")
		return
	}
	appIDLen := int(frame[7]) | int(frame[8])<<8
	headerSize := 9 + appIDLen

	if len(frame) < headerSize {
		n.logger.Error("frame too short for full header")
		return
	}

	header := decodeFrameHeader(frame[0:headerSize])
	val, ok := n.applications.Load(header.ApplicationID)
	if ok && val != nil {
		app := val.(*application)
		// Only send payload to application
		app.receiveWorker.InputChan <- frame[headerSize:]
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
