package nodosum

import (
	"bytes"
	"encoding/gob"
	"time"
)

type glutamatePacket struct {
	Version   int8
	Id        string
	ReceiveTs time.Time
	SendTs    time.Time
	Command   int
	Data      []byte
	Token     string
}

func pack(id string, cmd int, data []byte, token string) ([]byte, error) {
	packet := &glutamatePacket{
		Version: 1,
		Id:      id,
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

func unpack(packetData []byte) (*glutamatePacket, error) {
	var packet *glutamatePacket
	err := gob.NewDecoder(bytes.NewBuffer(packetData)).Decode(&packet)
	if err != nil {
		return nil, err
	}
	packet.ReceiveTs = time.Now()
	return packet, nil
}
