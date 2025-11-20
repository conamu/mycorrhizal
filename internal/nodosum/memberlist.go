package nodosum

import (
	"fmt"
	"net"
	"strconv"

	"github.com/hashicorp/memberlist"
)

type Delegate struct {
	*Nodosum
}

func (d Delegate) NotifyJoin(node *memberlist.Node) {
	addr, err := net.ResolveUDPAddr("udp", node.Addr.String()+":"+strconv.Itoa(d.quicPort))

	ln, err := d.quicTransport.Dial(d.ctx, addr, d.tlsConfig, d.quicConfig)
	if err != nil {
		d.logger.Error(fmt.Sprintf("error accepting quic connection: %s", err.Error()))
	}

	if ln != nil {
		d.logger.Debug(ln.RemoteAddr().String())
	}

	d.logger.Debug(fmt.Sprintf("quic connection established: %s", node.Name))
}

func (d Delegate) NotifyLeave(node *memberlist.Node) {
	d.logger.Debug(fmt.Sprintf("node left: %s", node.Name))
}

func (d Delegate) NotifyUpdate(node *memberlist.Node) {
	d.logger.Debug(fmt.Sprintf("node changed state: %s", node.Name))
}
