package mycel

import (
	"bytes"
	"encoding/gob"
	"errors"
	"time"
)

// geoPayload is the wire format for geo replication messages sent via app.Send().
// ExpiresAt carries the absolute expiry time so that receiving nodes apply the
// correct remaining TTL regardless of network delay.  TTL is kept for
// backward-compatibility but ignored on receive when ExpiresAt is non-zero.
type geoPayload struct {
	Operation  uint8
	Bucket     string
	UserID     string
	Lat        float64
	Lng        float64
	TTL        time.Duration // deprecated: use ExpiresAt
	ExpiresAt  time.Time    // absolute expiry; zero means "use bucket default"
	Precisions []uint       // carried on GEO_SET so receiving nodes can init the store if needed
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

	// Gather precisions and the resolved expiry from the local store so peers
	// apply the same absolute deadline (not a fresh duration from receipt time).
	s, err := g.getStore(bucket)
	if err != nil {
		return err
	}
	s.RLock()
	precisions := make([]uint, len(s.precisions))
	copy(precisions, s.precisions)
	var expiresAt time.Time
	if entry, ok := s.points[userID]; ok {
		expiresAt = entry.ExpiresAt
	}
	s.RUnlock()

	// 2. Broadcast to all other nodes — fire-and-forget via DATA frames.
	payload, err := encodeGeoPayload(geoPayload{
		Operation:  GEO_SET,
		Bucket:     bucket,
		UserID:     userID,
		Lat:        lat,
		Lng:        lng,
		ExpiresAt:  expiresAt,
		Precisions: precisions,
	})
	if err != nil {
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
// If the entry is absent locally (ERR_USER_NOT_FOUND / ERR_NO_GEO_BUCKET) the
// broadcast is still sent so peers converge to the deleted state.
func (g *geoCache) DeleteLocation(bucket, userID string) error {
	if err := g.deleteLocationLocal(bucket, userID); err != nil {
		if !errors.Is(err, ERR_USER_NOT_FOUND) && !errors.Is(err, ERR_NO_GEO_BUCKET) {
			return err
		}
		// Local state is already absent — still broadcast so peers converge.
		g.logger.Debug("geo: local delete missed, broadcasting anyway", "bucket", bucket, "user", userID)
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
		// Ensure the store exists on this node with the correct precision config.
		// createStore is a no-op if the store was already created via CreateGeoBucket.
		if len(p.Precisions) > 0 {
			if err := c.geo.createStore(p.Bucket, p.Precisions); err != nil {
				c.logger.Warn("geo: failed to init store for replicated bucket", "bucket", p.Bucket, "error", err)
			}
		}
		// Derive remaining TTL:
		//  1. Prefer ExpiresAt (absolute deadline set by current senders) — this
		//     avoids extending the entry lifetime by network delay.
		//  2. Fall back to p.TTL for payloads from older nodes that predate the
		//     ExpiresAt field (mixed-version clusters or zero-ttl writes).
		//  A zero remainingTTL means "use bucket default" on the receiving side.
		var remainingTTL time.Duration
		if !p.ExpiresAt.IsZero() {
			remainingTTL = time.Until(p.ExpiresAt)
			if remainingTTL <= 0 {
				// Already expired in transit — skip the write entirely.
				c.logger.Debug("geo: dropping expired replicated SET", "bucket", p.Bucket, "user", p.UserID)
				return nil
			}
		} else if p.TTL > 0 {
			// Legacy payload: ExpiresAt not set, honour the raw TTL duration.
			remainingTTL = p.TTL
		}
		return c.geo.setLocationLocal(p.Bucket, p.UserID, p.Lat, p.Lng, remainingTTL)
	case DELETE:
		c.logger.Debug("geo: received replicated DELETE", "bucket", p.Bucket, "user", p.UserID)
		return c.geo.deleteLocationLocal(p.Bucket, p.UserID)
	default:
		c.logger.Warn("geo: unknown operation in receive payload", "op", p.Operation)
	}
	return nil
}
