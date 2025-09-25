package nodosum

import (
	"bytes"
	"encoding/gob"
	"time"
)

type glutamatePacket struct {
	Version   int8
	ReceiveTs time.Time
	SendTs    time.Time
	Command   int
	Data      []byte
	Token     string
}

func pack(cmd int, data []byte, token string) ([]byte, error) {
	packet := &glutamatePacket{
		Version: 1,
		SendTs:  time.Now(),
		Command: cmd,
		Data:    data,
	}

	packet.Token = token

	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(packet)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unpack(data []byte) (*glutamatePacket, error) {
	var packet *glutamatePacket
	err := gob.NewDecoder(bytes.NewBuffer(data)).Decode(&packet)
	if err != nil {
		return nil, err
	}
	packet.ReceiveTs = time.Now()
	return packet, nil
}
