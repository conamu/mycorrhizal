package nodosum

import (
	"errors"

	"github.com/conamu/mycorrizal/internal/packet"
	"github.com/google/uuid"
)

var dataSets = map[string][]byte{}

const (
	// System
	HELLO = iota
	DENY
	REPLY
	// Usable
	ID
	EXIT
	SET
	GET
)

func (n *Nodosum) handler(pack *packet.GlutamatePacket, writeChan chan []byte) func() (bool, error) {
	switch pack.Command {
	case ID:
		return func() (bool, error) {
			p, err := packet.Pack(REPLY, []byte(n.nodeId), pack.Token)
			if err != nil {
				return false, errors.Join(errors.New("error packing glutamate packet"), err)
			}
			writeChan <- p
			return false, nil
		}
	case EXIT:
		return func() (bool, error) {
			return true, nil
		}
	case SET:
		return func() (bool, error) {
			id := uuid.NewString()
			dataSets[id] = pack.Data
			p, err := packet.Pack(REPLY, []byte(id), pack.Token)
			if err != nil {
				return false, errors.Join(errors.New("error packing glutamate packet"), err)
			}
			writeChan <- p
			return false, nil
		}
	case GET:
		return func() (bool, error) {
			stuff := []byte{}
			if d, ok := dataSets[string(pack.Data)]; ok {
				stuff = d
			}

			p, err := packet.Pack(REPLY, stuff, pack.Token)
			if err != nil {
				return false, errors.Join(errors.New("error packing glutamate packet"), err)
			}
			writeChan <- p
			return false, nil
		}
	default:
		return nil
	}
}
