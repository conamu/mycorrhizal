package packet

import (
	"bytes"
	"encoding/gob"
	"time"
)

type GlutamatePacket struct {
	Version   int8
	ReceiveTs time.Time
	SendTs    time.Time
	Command   string
	Data      []byte
}

func Pack(cmd string, data []byte) ([]byte, error) {
	packet := &GlutamatePacket{
		Version: 1,
		SendTs:  time.Now(),
		Command: cmd,
		Data:    data,
	}

	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(packet)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Unpack(data []byte) (string, []byte, error) {
	var packet *GlutamatePacket
	err := gob.NewDecoder(bytes.NewBuffer(data)).Decode(&packet)
	if err != nil {
		return "", nil, err
	}
	packet.ReceiveTs = time.Now()
	return packet.Command, packet.Data, nil
}
