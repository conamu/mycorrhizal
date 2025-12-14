package nodosum

import (
	"encoding/binary"
	"io"
)

// Frame types for QUIC stream protocol
const (
	FRAME_TYPE_STREAM_INIT = 0x01 // First message on a new stream
	FRAME_TYPE_DATA        = 0x02 // Subsequent data messages
	FRAME_TYPE_REQUEST     = 0x03
	FRAME_TYPE_RESPONSE    = 0x04
)

type RequestFrame struct {
	CorrelationID string
	Payload       []byte
}

type ResponseFrame struct {
	CorrelationID string
	Payload       []byte
	Error         string
}

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

// encodeRequestFrame creates a request frame for sending request/response pattern messages.
//
// Frame format:
// [1 byte: frame type (REQUEST)]
// [2 bytes: correlationID length (big-endian uint16)]
// [N bytes: correlationID (UTF-8 string)]
// [4 bytes: payload length (big-endian uint32)]
// [N bytes: payload]
//
// Example:
//
//	reqFrame := RequestFrame{
//	    CorrelationID: "uuid-123",
//	    Payload: []byte("request data"),
//	}
//	frame := encodeRequestFrame(reqFrame)
//	stream.Write(frame)
func encodeRequestFrame(req RequestFrame) []byte {
	buf := make([]byte, 0, 1+2+len(req.CorrelationID)+4+len(req.Payload))

	// Frame type
	buf = append(buf, FRAME_TYPE_REQUEST)

	// CorrelationID length and value
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(req.CorrelationID)))
	buf = append(buf, []byte(req.CorrelationID)...)

	// Payload length and value
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(req.Payload)))
	buf = append(buf, req.Payload...)

	return buf
}

// decodeRequestFrame decodes a request frame from raw bytes.
// The frameData should NOT include the frame type byte (already read).
//
// Example:
//
//	var reqFrame RequestFrame
//	err := decodeRequestFrame(payload, &reqFrame)
//	if err != nil {
//	    return err
//	}
func decodeRequestFrame(frameData []byte, req *RequestFrame) error {
	if len(frameData) < 2 {
		return io.ErrUnexpectedEOF
	}

	offset := 0

	// Read correlationID length
	correlationIDLen := binary.BigEndian.Uint16(frameData[offset : offset+2])
	offset += 2

	if len(frameData) < offset+int(correlationIDLen) {
		return io.ErrUnexpectedEOF
	}

	// Read correlationID
	req.CorrelationID = string(frameData[offset : offset+int(correlationIDLen)])
	offset += int(correlationIDLen)

	if len(frameData) < offset+4 {
		return io.ErrUnexpectedEOF
	}

	// Read payload length
	payloadLen := binary.BigEndian.Uint32(frameData[offset : offset+4])
	offset += 4

	if len(frameData) < offset+int(payloadLen) {
		return io.ErrUnexpectedEOF
	}

	// Read payload
	req.Payload = frameData[offset : offset+int(payloadLen)]

	return nil
}

// encodeResponseFrame creates a response frame for sending request/response pattern messages.
//
// Frame format:
// [1 byte: frame type (RESPONSE)]
// [2 bytes: correlationID length (big-endian uint16)]
// [N bytes: correlationID (UTF-8 string)]
// [2 bytes: error length (big-endian uint16)]
// [N bytes: error (UTF-8 string, empty if no error)]
// [4 bytes: payload length (big-endian uint32)]
// [N bytes: payload]
//
// Example:
//
//	respFrame := ResponseFrame{
//	    CorrelationID: "uuid-123",
//	    Payload: []byte("response data"),
//	    Error: "",
//	}
//	frame := encodeResponseFrame(respFrame)
//	stream.Write(frame)
func encodeResponseFrame(resp ResponseFrame) []byte {
	buf := make([]byte, 0, 1+2+len(resp.CorrelationID)+2+len(resp.Error)+4+len(resp.Payload))

	// Frame type
	buf = append(buf, FRAME_TYPE_RESPONSE)

	// CorrelationID length and value
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(resp.CorrelationID)))
	buf = append(buf, []byte(resp.CorrelationID)...)

	// Error length and value
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(resp.Error)))
	if len(resp.Error) > 0 {
		buf = append(buf, []byte(resp.Error)...)
	}

	// Payload length and value
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(resp.Payload)))
	buf = append(buf, resp.Payload...)

	return buf
}

// decodeResponseFrame decodes a response frame from raw bytes.
// The frameData should NOT include the frame type byte (already read).
//
// Example:
//
//	var respFrame ResponseFrame
//	err := decodeResponseFrame(payload, &respFrame)
//	if err != nil {
//	    return err
//	}
func decodeResponseFrame(frameData []byte, resp *ResponseFrame) error {
	if len(frameData) < 2 {
		return io.ErrUnexpectedEOF
	}

	offset := 0

	// Read correlationID length
	correlationIDLen := binary.BigEndian.Uint16(frameData[offset : offset+2])
	offset += 2

	if len(frameData) < offset+int(correlationIDLen) {
		return io.ErrUnexpectedEOF
	}

	// Read correlationID
	resp.CorrelationID = string(frameData[offset : offset+int(correlationIDLen)])
	offset += int(correlationIDLen)

	if len(frameData) < offset+2 {
		return io.ErrUnexpectedEOF
	}

	// Read error length
	errorLen := binary.BigEndian.Uint16(frameData[offset : offset+2])
	offset += 2

	if len(frameData) < offset+int(errorLen) {
		return io.ErrUnexpectedEOF
	}

	// Read error
	if errorLen > 0 {
		resp.Error = string(frameData[offset : offset+int(errorLen)])
		offset += int(errorLen)
	}

	if len(frameData) < offset+4 {
		return io.ErrUnexpectedEOF
	}

	// Read payload length
	payloadLen := binary.BigEndian.Uint32(frameData[offset : offset+4])
	offset += 4

	if len(frameData) < offset+int(payloadLen) {
		return io.ErrUnexpectedEOF
	}

	// Read payload
	resp.Payload = frameData[offset : offset+int(payloadLen)]

	return nil
}

// sendResponseFrame writes a response frame to a QUIC stream.
// This is a helper function for responding to requests.
//
// Example:
//
//	respFrame := ResponseFrame{
//	    CorrelationID: reqFrame.CorrelationID,
//	    Payload: []byte("response"),
//	}
//	err := sendResponseFrame(stream, respFrame)
func sendResponseFrame(stream io.Writer, resp ResponseFrame) error {
	frame := encodeResponseFrame(resp)
	_, err := stream.Write(frame)
	return err
}

// readFrame reads the next frame from a QUIC stream.
// Returns the frame type and payload (for DATA, REQUEST, and RESPONSE frames).
//
// The caller is responsible for handling different frame types:
// - STREAM_INIT: Should only appear as the first frame, call decodeStreamInit separately
// - DATA: Payload is returned in the payload return value
// - REQUEST/RESPONSE: Payload contains the encoded frame data (without type byte)
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
//	    switch frameType {
//	    case FRAME_TYPE_DATA:
//	        handlePayload(payload)
//	    case FRAME_TYPE_REQUEST:
//	        var req RequestFrame
//	        decodeRequestFrame(payload, &req)
//	        handleRequest(req)
//	    case FRAME_TYPE_RESPONSE:
//	        var resp ResponseFrame
//	        decodeResponseFrame(payload, &resp)
//	        handleResponse(resp)
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

	// For REQUEST and RESPONSE frames, read the entire frame data
	if frameType == FRAME_TYPE_REQUEST || frameType == FRAME_TYPE_RESPONSE {
		// We need to peek ahead to determine the total frame size
		// For simplicity, read in chunks and reconstruct

		// Read correlationID length
		lenBuf := make([]byte, 2)
		if _, err := io.ReadFull(stream, lenBuf); err != nil {
			return 0, nil, err
		}
		correlationIDLen := binary.BigEndian.Uint16(lenBuf)

		// Start building the payload (everything after frame type)
		payload = make([]byte, 0, 2+int(correlationIDLen)+100) // Initial capacity estimate
		payload = append(payload, lenBuf...)

		// Read correlationID
		correlationID := make([]byte, correlationIDLen)
		if _, err := io.ReadFull(stream, correlationID); err != nil {
			return 0, nil, err
		}
		payload = append(payload, correlationID...)

		if frameType == FRAME_TYPE_RESPONSE {
			// Read error length
			if _, err := io.ReadFull(stream, lenBuf); err != nil {
				return 0, nil, err
			}
			errorLen := binary.BigEndian.Uint16(lenBuf)
			payload = append(payload, lenBuf...)

			// Read error string
			if errorLen > 0 {
				errorStr := make([]byte, errorLen)
				if _, err := io.ReadFull(stream, errorStr); err != nil {
					return 0, nil, err
				}
				payload = append(payload, errorStr...)
			}
		}

		// Read payload length
		payloadLenBuf := make([]byte, 4)
		if _, err := io.ReadFull(stream, payloadLenBuf); err != nil {
			return 0, nil, err
		}
		payloadDataLen := binary.BigEndian.Uint32(payloadLenBuf)
		payload = append(payload, payloadLenBuf...)

		// Read payload data
		if payloadDataLen > 0 {
			payloadData := make([]byte, payloadDataLen)
			if _, err := io.ReadFull(stream, payloadData); err != nil {
				return 0, nil, err
			}
			payload = append(payload, payloadData...)
		}

		return frameType, payload, nil
	}

	// Unknown frame type - return it but no payload
	return frameType, nil, nil
}

func (n *Nodosum) getConnectedNodes() []string {
	ids := make([]string, 0)

	n.quicConns.RLock()
	for id := range n.quicConns.conns {
		ids = append(ids, id)
	}
	n.quicConns.RUnlock()

	return ids
}
