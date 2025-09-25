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
	Command   int
	Data      []byte
	Token     string
}

func Pack(cmd int, data []byte, token string) ([]byte, error) {
	packet := &GlutamatePacket{
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

func Unpack(data []byte) (*GlutamatePacket, error) {
	var packet *GlutamatePacket
	err := gob.NewDecoder(bytes.NewBuffer(data)).Decode(&packet)
	if err != nil {
		return nil, err
	}
	packet.ReceiveTs = time.Now()
	return packet, nil
}
