package nodosum

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"net"
)

/*

This is the implementation of the Glutamate Protocol
For controlling, connecting and enabling data transfer in Mycorrizal Clusters.

The protocol itself is designed to transmit any kind of data efficiently to one or more cluster members.

An 11 byte frame header is sent first.
For a payload bigger than 50kb compression should be used by using the COMPRESSED flag.

The application id is used to route data to the corresponding subsystem registered with the Application API.

Overall usage contains:
	- absolute data syncs when a new node joins the cluster
	- request a data resource that is not present locally but may be present on a different node
	- sending commands from CLI -> Node or Node -> Node. (PING, SYNC, HEALTHCHECK, etc...)

This is not designed to be the absolute high performance cluster protocol
but it tries to adhere to good performance standards to at least leverage an own implementation.
This includes optional compression, Multiplexing readiness, shared buffers and direct binary encoding.

*/

type messageFlag uint8
type messageType uint8

const (
	COMPRESSED messageFlag = iota
	CONN_INIT_TOKEN
	CONN_INIT
)

const (
	SYSTEM messageType = iota
	APP
)

type frameHeader struct {
	Version       uint8
	ApplicationID string      // ID for multiplexer to route to subsystem
	Type          messageType // message type (e.g. DATA, CONTROL, PING, etc.)
	Flag          messageFlag // optional flag for behavior
	Length        uint32      // length of payload following this header
}

func encodeFrameHeader(fh *frameHeader) []byte {
	buf := make([]byte, 12)

	buf[0] = fh.Version
	buf[1] = uint8(fh.Type)
	buf[2] = uint8(fh.Flag)
	binary.LittleEndian.PutUint32(buf[3:], fh.Length)
	buf = append(buf, []byte(fh.ApplicationID)...)

	return buf
}

func decodeFrameHeader(frameHeaderBytes []byte) *frameHeader {
	fh := frameHeader{}

	fh.Version = frameHeaderBytes[0]
	fh.Type = messageType(frameHeaderBytes[1])
	fh.Flag = messageFlag(frameHeaderBytes[2])
	fh.Length = binary.LittleEndian.Uint32(frameHeaderBytes[3:7])
	fh.ApplicationID = string(frameHeaderBytes[7:])

	return &fh
}

/*
	UDP handshake protocol
	1. Node ID exchange
	2. Secret verification
	3. A random conn init value is exchanged to decide who initiates connection
	4. The connection receiver sends a temporary one-time use key to establish
		a verified tcp connection after the handshake
*/

func (n *Nodosum) encodeHandshakePacket(hp *handshakeUdpPacket) []byte {
	buf := make([]byte, 3)

	buf[0] = hp.Version
	buf[1] = uint8(hp.Type)
	binary.LittleEndian.PutUint32(buf[2:6], n.connInit.val)

	idBytes := []byte(hp.Id)
	if len(idBytes) != 16 {
		return nil
	}

	secretBytes := []byte(hp.Secret)
	if len(secretBytes) != 64 {
		return nil
	}

	buf = append(buf, idBytes...)
	buf = append(buf, secretBytes...)

	return buf
}

func (n *Nodosum) decodeHandshakePacket(bytes []byte) *handshakeUdpPacket {
	hp := handshakeUdpPacket{}

	hp.Version = bytes[0]
	hp.Type = handshakeMessage(bytes[1])
	hp.ConnInit = binary.LittleEndian.Uint32(bytes[2:6])
	hp.Id = string(bytes[3:20])
	hp.Secret = string(bytes[20:85])

	return &hp
}

func (n *Nodosum) handleUdpPacket(hp *handshakeUdpPacket, addr string) *handshakeUdpPacket {
	switch hp.Type {
	case HELLO:
		n.logger.Debug("received hello packet", "remote-id", hp.Id, "id", n.nodeId)
		return &handshakeUdpPacket{
			Version:  1,
			Type:     HELLO_ACK,
			ConnInit: n.connInit.val,
			Id:       n.nodeId,
			Secret:   n.sharedSecret,
		}
	case HELLO_ACK:
		n.logger.Debug("received hello_ack packet", "remote-id", hp.Id, "id", n.nodeId)
		// The node with the lower value will send a connect packet, initializing the tcp connection with an otp token

		if hp.ConnInit < n.connInit.val {
			secret := make([]byte, 64)
			_, err := rand.Read(secret)
			if err != nil {
				n.logger.Error("failed generating random 64 byte connection secret")
			}
			n.connInit.tokens[hp.Id] = string(secret)

			return &handshakeUdpPacket{
				Version:  1,
				Type:     HELLO_CONN,
				ConnInit: n.connInit.val,
				Id:       n.nodeId,
				Secret:   string(secret),
			}
		}
		return nil
	case HELLO_CONN:
		n.logger.Debug("received hello_conn packet", "remote-id", hp.Id, "id", n.nodeId)
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			n.logger.Error("failed connecting to server")
			return nil
		}

		if n.tlsEnabled {
			conn = tls.Client(conn, n.tlsConfig)
		}

		n.createConnChannel(hp.Id, conn)

		connectionToken := []byte(hp.Secret)

		bytes := encodeFrameHeader(&frameHeader{
			Version:       1,
			ApplicationID: n.nodeId,
			Type:          SYSTEM,
			Flag:          CONN_INIT_TOKEN,
			Length:        uint32(len(connectionToken)),
		})

		readBuf := make([]byte, 1024)
		i, err := conn.Read(readBuf)
		if err != nil {
			n.handleConnError(err, hp.Id)
		}

		fh := decodeFrameHeader(readBuf[:i])
		if fh.Type != SYSTEM || fh.Flag != CONN_INIT {
			return nil
		}

		_, err = conn.Write(bytes)
		_, err = conn.Write(connectionToken)

		n.startRwLoops(fh.ApplicationID)
	}
	return nil
}

type handshakeUdpPacket struct {
	Version  uint8
	Type     handshakeMessage
	ConnInit uint32
	Id       string
	Secret   string
}

type handshakeMessage uint8

const (
	HELLO handshakeMessage = iota
	HELLO_ACK
	HELLO_CONN
)
