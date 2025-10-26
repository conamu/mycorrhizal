package nodosum

import (
	"testing"
)

func TestEncodeFrameHeader(t *testing.T) {
	fh := &frameHeader{
		Version:       1,
		ApplicationID: 0x12345678,
		Type:          SYSTEM,
		Flag:          COMPRESSED,
		Length:        1024,
	}

	encoded := encodeFrameHeader(fh)

	if len(encoded) != 11 {
		t.Errorf("Expected encoded frame header length to be 11, got %d", len(encoded))
	}

	if encoded[0] != 1 {
		t.Errorf("Expected version to be 1, got %d", encoded[0])
	}

	if encoded[6] != uint8(SYSTEM) {
		t.Errorf("Expected type to be %d, got %d", SYSTEM, encoded[6])
	}

	if encoded[7] != uint8(COMPRESSED) {
		t.Errorf("Expected flag to be %d, got %d", COMPRESSED, encoded[7])
	}
}

func TestDecodeFrameHeader(t *testing.T) {
	frameBytes := []byte{
		1,                      // Version
		0x78, 0x56, 0x34, 0x12, // ApplicationID (little endian)
		uint8(APP),             // Type
		uint8(COMPRESSED),      // Flag
		0x00, 0x04, 0x00, 0x00, // Length (1024 in little endian)
	}

	decoded := decodeFrameHeader(frameBytes)

	if decoded.Version != 1 {
		t.Errorf("Expected version to be 1, got %d", decoded.Version)
	}

	if decoded.ApplicationID != 0x12345678 {
		t.Errorf("Expected ApplicationID to be 0x12345678, got 0x%08x", decoded.ApplicationID)
	}

	if decoded.Type != APP {
		t.Errorf("Expected type to be %d, got %d", APP, decoded.Type)
	}

	if decoded.Flag != COMPRESSED {
		t.Errorf("Expected flag to be %d, got %d", COMPRESSED, decoded.Flag)
	}

	if decoded.Length != 1024 {
		t.Errorf("Expected length to be 1024, got %d", decoded.Length)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := &frameHeader{
		Version:       2,
		ApplicationID: 0xABCDEF00,
		Type:          APP,
		Flag:          0,
		Length:        65536,
	}

	encoded := encodeFrameHeader(original)
	decoded := decodeFrameHeader(encoded)

	if decoded.Version != original.Version {
		t.Errorf("Version mismatch: expected %d, got %d", original.Version, decoded.Version)
	}

	if decoded.ApplicationID != original.ApplicationID {
		t.Errorf("ApplicationID mismatch: expected 0x%08x, got 0x%08x", original.ApplicationID, decoded.ApplicationID)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type mismatch: expected %d, got %d", original.Type, decoded.Type)
	}

	if decoded.Flag != original.Flag {
		t.Errorf("Flag mismatch: expected %d, got %d", original.Flag, decoded.Flag)
	}

	if decoded.Length != original.Length {
		t.Errorf("Length mismatch: expected %d, got %d", original.Length, decoded.Length)
	}
}
