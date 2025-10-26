package nodosum

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
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
)

const (
	SYSTEM messageType = iota
	APP
)

type frameHeader struct {
	Version       uint8
	ApplicationID uint32      // ID for multiplexer to route to subsystem
	Type          messageType // message type (e.g. DATA, CONTROL, PING, etc.)
	Flag          messageFlag // optional flag for behavior
	Length        uint32      // length of payload following this header
}

func encodeFrameHeader(fh *frameHeader) []byte {
	buf := make([]byte, 11)

	buf[0] = fh.Version
	binary.LittleEndian.PutUint32(buf[1:], fh.ApplicationID)
	buf[6] = uint8(fh.Type)
	buf[7] = uint8(fh.Flag)
	binary.LittleEndian.PutUint32(buf[8:], fh.Length)

	return buf
}

func decodeFrameHeader(frameHeaderBytes []byte) *frameHeader {
	fh := frameHeader{}

	fh.Version = frameHeaderBytes[0]
	fh.ApplicationID = binary.LittleEndian.Uint32(frameHeaderBytes[1:5])
	fh.Type = messageType(frameHeaderBytes[6])
	fh.Flag = messageFlag(frameHeaderBytes[7])
	fh.Length = binary.LittleEndian.Uint32(frameHeaderBytes[8:11])

	return &fh
}
