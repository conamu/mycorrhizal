package mycel

import (
	"bytes"
	"encoding/gob"
	"time"
)

type remoteCachePayload struct {
	Operation uint8
	Key       string
	Bucket    string
	Value     any
	Ttl       time.Duration
}

func (c *cache) gobEncode(payload remoteCachePayload) ([]byte, error) {
	buf := new(bytes.Buffer)

	enc := gob.NewEncoder(buf)

	err := enc.Encode(payload)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *cache) gobDecode(payload []byte) (*remoteCachePayload, error) {
	buf := bytes.NewBuffer(payload)

	dec := gob.NewDecoder(buf)

	result := new(remoteCachePayload)

	err := dec.Decode(result)
	if err != nil {
		return nil, err
	}
	return result, nil
}
