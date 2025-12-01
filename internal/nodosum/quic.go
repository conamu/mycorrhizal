package nodosum

import (
	"encoding/binary"
	"io"
)

// Frame types for QUIC stream protocol
const (
	FRAME_TYPE_STREAM_INIT = 0x01 // First message on a new stream
	FRAME_TYPE_DATA        = 0x02 // Subsequent data messages
)

// encodeStreamInit creates a stream initialization frame.
// This frame is sent as the first message on a new QUIC stream to identify
// the sender node, application, and stream name.
//
// Frame format:
// [1 byte: frame type (STREAM_INIT)]
// [2 bytes: nodeID length (big-endian uint16)]
// [N bytes: nodeID (UTF-8 string)]
// [2 bytes: appID length (big-endian uint16)]
// [N bytes: appID (UTF-8 string)]
// [2 bytes: streamName length (big-endian uint16)]
// [N bytes: streamName (UTF-8 string)]
//
// Example:
//
//	frame := encodeStreamInit("node-123", "SYSTEM-MYCEL", "bucket:sessions")
//	stream.Write(frame)
func encodeStreamInit(nodeID, appID, streamName string) []byte {
	// Calculate total size
	size := 1 + 2 + len(nodeID) + 2 + len(appID) + 2 + len(streamName)
	buf := make([]byte, 0, size)

	// Frame type
	buf = append(buf, FRAME_TYPE_STREAM_INIT)

	// Node ID length and value
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(nodeID)))
	buf = append(buf, []byte(nodeID)...)

	// Application ID length and value
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(appID)))
	buf = append(buf, []byte(appID)...)

	// Stream name length and value
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(streamName)))
	buf = append(buf, []byte(streamName)...)

	return buf
}

// decodeStreamInit reads and decodes a stream initialization frame from a QUIC stream.
// This should be called immediately after accepting a new stream to identify its purpose.
//
// The function expects the frame type byte (STREAM_INIT) to have already been read.
// It then reads the nodeID, appID, and streamName from the stream.
//
// Returns the node ID, application ID, stream name, and any error encountered.
//
// Example:
//
//	// After accepting stream and reading frame type
//	nodeID, appID, streamName, err := decodeStreamInit(stream)
//	if err != nil {
//	    return err
//	}
//	log.Printf("Stream from node %s for app %s, stream %s", nodeID, appID, streamName)
func decodeStreamInit(stream io.Reader) (nodeID, appID, streamName string, err error) {
	lenBuf := make([]byte, 2)

	// Read nodeID length
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		return "", "", "", err
	}
	nodeIDLen := binary.BigEndian.Uint16(lenBuf)

	// Read nodeID
	nodeIDBuf := make([]byte, nodeIDLen)
	if _, err := io.ReadFull(stream, nodeIDBuf); err != nil {
		return "", "", "", err
	}
	nodeID = string(nodeIDBuf)

	// Read appID length
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		return "", "", "", err
	}
	appIDLen := binary.BigEndian.Uint16(lenBuf)

	// Read appID
	appIDBuf := make([]byte, appIDLen)
	if _, err := io.ReadFull(stream, appIDBuf); err != nil {
		return "", "", "", err
	}
	appID = string(appIDBuf)

	// Read streamName length
	if _, err := io.ReadFull(stream, lenBuf); err != nil {
		return "", "", "", err
	}
	streamNameLen := binary.BigEndian.Uint16(lenBuf)

	// Read streamName
	streamNameBuf := make([]byte, streamNameLen)
	if _, err := io.ReadFull(stream, streamNameBuf); err != nil {
		return "", "", "", err
	}
	streamName = string(streamNameBuf)

	return nodeID, appID, streamName, nil
}

// encodeDataFrame creates a data frame for sending application payload.
// This is used for all messages after the initial stream init frame.
//
// Frame format:
// [1 byte: frame type (DATA)]
// [4 bytes: payload length (big-endian uint32)]
// [N bytes: payload]
//
// Example:
//
//	frame := encodeDataFrame([]byte("hello world"))
//	stream.Write(frame)
func encodeDataFrame(payload []byte) []byte {
	buf := make([]byte, 0, 1+4+len(payload))

	// Frame type
	buf = append(buf, FRAME_TYPE_DATA)

	// Payload length and value
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)

	return buf
}

// readFrame reads the next frame from a QUIC stream.
// Returns the frame type and payload (for DATA frames).
//
// The caller is responsible for handling different frame types:
// - STREAM_INIT: Should only appear as the first frame, call decodeStreamInit separately
// - DATA: Payload is returned in the payload return value
//
// Returns io.EOF when the stream is closed gracefully.
//
// Example:
//
//	for {
//	    frameType, payload, err := readFrame(stream)
//	    if err == io.EOF {
//	        break  // Stream closed
//	    }
//	    if err != nil {
//	        return err
//	    }
//	    if frameType == FRAME_TYPE_DATA {
//	        handlePayload(payload)
//	    }
//	}
func readFrame(stream io.Reader) (frameType byte, payload []byte, err error) {
	// Read frame type
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(stream, typeBuf); err != nil {
		return 0, nil, err
	}
	frameType = typeBuf[0]

	// STREAM_INIT frames should be handled separately with decodeStreamInit
	if frameType == FRAME_TYPE_STREAM_INIT {
		// Caller should handle this specially
		return frameType, nil, nil
	}

	// For DATA frames, read the payload
	if frameType == FRAME_TYPE_DATA {
		// Read payload length
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(stream, lenBuf); err != nil {
			return 0, nil, err
		}
		payloadLen := binary.BigEndian.Uint32(lenBuf)

		// Read payload
		payload = make([]byte, payloadLen)
		if _, err := io.ReadFull(stream, payload); err != nil {
			return 0, nil, err
		}

		return frameType, payload, nil
	}

	// Unknown frame type - return it but no payload
	return frameType, nil, nil
}
