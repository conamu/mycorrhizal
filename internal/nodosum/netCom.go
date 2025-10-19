package nodosum

import (
	"errors"

	"github.com/google/uuid"
)

/* TODO:
Following problem:
There is only ever one connection between two nodes. Glutamate packets need to have the ability to be "multiplexed"
so multiple application sub-systems can use this connection, via an ID based or v-channel based system.
IDEA:
Attach an ID to each packet. One ID has exactly one Command send and one receive. One Question one answer.
One concept to be explored is potentially also one ID enabling a chain, like a channel of sorts
on top of that connection.
*/

var ComErrUnauthorized = errors.New("not authorized to perform this operation")

type Command interface {
	packAndSend(writeChan chan []byte) error
	receiveAndUnpack(readChan chan []byte) error
}
type command struct {
	id       string
	senderId string
	command  int
	data     []byte
	aclToken string
}

func (n *Nodosum) NewCommand(cmd int, data []byte, token string) Command {
	id := uuid.NewString()
	return &command{
		id:       id,
		senderId: n.nodeId,
		command:  cmd,
		data:     data,
		aclToken: token,
	}
}

func (c *command) packAndSend(writeChan chan []byte) error {
	packet, err := pack(c.id, c.command, c.data, c.aclToken)
	if err != nil {
		return err
	}
	writeChan <- packet
	return nil
}

func (c *command) receiveAndUnpack(readChan chan []byte) error {
	packet := <-readChan
	unPacket, err := unpack(packet)
	if err != nil {
		return err
	}
	if unPacket.Command == DENY {
		return ComErrUnauthorized
	}
	c.command = unPacket.Command
	c.data = unPacket.Data
	c.aclToken = unPacket.Token

	return nil
}

// Create command objects that can marshall into glutmate packets.
// This serves as outside interface for other components to use the nodosum component

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

func (n *Nodosum) handler(packet *glutamatePacket, writeChan chan []byte) func() (bool, error) {
	switch packet.Command {
	case ID:
		return func() (bool, error) {
			p, err := pack("ID", REPLY, []byte(n.nodeId), packet.Token)
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
			dataSets[id] = packet.Data
			p, err := pack("SET", REPLY, []byte(id), packet.Token)
			if err != nil {
				return false, errors.Join(errors.New("error packing glutamate packet"), err)
			}
			writeChan <- p
			return false, nil
		}
	case GET:
		return func() (bool, error) {
			stuff := []byte{}
			if d, ok := dataSets[string(packet.Data)]; ok {
				stuff = d
			}

			p, err := pack("GET", REPLY, stuff, packet.Token)
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
