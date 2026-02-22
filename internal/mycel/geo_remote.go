package mycel

import (
	"bytes"
	"encoding/gob"
	"time"
)

// geoPayload is the wire format for geo replication messages sent via app.Send().
type geoPayload struct {
	Operation uint8
	Bucket    string
	UserID    string
	Lat       float64
	Lng       float64
	TTL       time.Duration
}

func encodeGeoPayload(p geoPayload) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeGeoPayload(data []byte) (*geoPayload, error) {
	p := new(geoPayload)
	if err := gob.NewDecoder(bytes.NewBuffer(data)).Decode(p); err != nil {
		return nil, err
	}
	return p, nil
}

// SetLocation writes the location locally then broadcasts to all other nodes.
func (g *geoCache) SetLocation(bucket, userID string, lat, lng float64, ttl time.Duration) error {
	// 1. Write locally first.
	if err := g.setLocationLocal(bucket, userID, lat, lng, ttl); err != nil {
		return err
	}

	// 2. Broadcast to all other nodes — fire-and-forget via DATA frames.
	payload, err := encodeGeoPayload(geoPayload{
		Operation: GEO_SET,
		Bucket:    bucket,
		UserID:    userID,
		Lat:       lat,
		Lng:       lng,
		TTL:       ttl,
	})
	if err != nil {
		// Encoding failure is a local bug; log but don't block the caller.
		g.logger.Error("geo: failed to encode replication payload", "error", err)
		return err
	}

	// Empty ids slice = broadcast to all connected nodes.
	if err := g.app.Send(payload, []string{}); err != nil {
		g.logger.Warn("geo: replication broadcast failed", "error", err)
		// Non-fatal: local write succeeded; other nodes will reconcile on next update.
	}

	return nil
}

// DeleteLocation removes a user's location locally and broadcasts the deletion.
func (g *geoCache) DeleteLocation(bucket, userID string) error {
	if err := g.deleteLocationLocal(bucket, userID); err != nil {
		return err
	}

	payload, err := encodeGeoPayload(geoPayload{
		Operation: DELETE,
		Bucket:    bucket,
		UserID:    userID,
	})
	if err != nil {
		g.logger.Error("geo: failed to encode delete replication payload", "error", err)
		return err
	}

	if err := g.app.Send(payload, []string{}); err != nil {
		g.logger.Warn("geo: delete replication broadcast failed", "error", err)
	}

	return nil
}

// handleGeoReceive is called by applicationReceiveFunc when a GEO_SET or
// DELETE DATA frame arrives from another node.
func (c *cache) handleGeoReceive(data []byte) error {
	p, err := decodeGeoPayload(data)
	if err != nil {
		return err
	}

	switch p.Operation {
	case GEO_SET:
		c.logger.Debug("geo: received replicated SET", "bucket", p.Bucket, "user", p.UserID)
		return c.geo.setLocationLocal(p.Bucket, p.UserID, p.Lat, p.Lng, p.TTL)
	case DELETE:
		c.logger.Debug("geo: received replicated DELETE", "bucket", p.Bucket, "user", p.UserID)
		return c.geo.deleteLocationLocal(p.Bucket, p.UserID)
	default:
		c.logger.Warn("geo: unknown operation in receive payload", "op", p.Operation)
	}
	return nil
}
