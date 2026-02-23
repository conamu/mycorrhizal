package mycel

import (
	"math"
	"time"

	"github.com/mmcloughlin/geohash"
)

// setLocationLocal writes a user's location into the local geoStore at every
// configured precision level, and into the regular LRU/keyVal store so the
// existing TTL eviction worker handles expiry for free.
func (g *geoCache) setLocationLocal(bucket, userID string, lat, lng float64, ttl time.Duration) error {
	s, err := g.getStore(bucket)
	if err != nil {
		return err
	}

	// Resolve the absolute expiry now so it can be stored in GeoEntry and
	// broadcast to peers as an absolute deadline rather than a relative duration.
	// A zero ttl means "use bucket default" and ExpiresAt stays zero.
	var expiresAt time.Time
	if ttl != 0 {
		expiresAt = time.Now().Add(ttl)
	}

	// Encode at the highest precision — this is the canonical geohash stored
	// in GeoEntry and used for exact coordinate lookups.
	maxPrec := s.maxPrecision()
	canonicalCell := geohash.EncodeWithPrecision(lat, lng, maxPrec)

	entry := GeoEntry{
		UserID:    userID,
		Lat:       lat,
		Lng:       lng,
		Geohash:   canonicalCell,
		ExpiresAt: expiresAt,
	}

	s.Lock()

	// If the user already exists and has moved, remove them from every precision
	// tier's old cell before re-inserting at the new position.
	if old, exists := s.points[userID]; exists && old.Geohash != canonicalCell {
		for _, p := range s.precisions {
			oldCell := geohash.EncodeWithPrecision(old.Lat, old.Lng, p)
			tier := s.index[p]
			if ids, ok := tier[oldCell]; ok {
				delete(ids, userID)
				if len(ids) == 0 {
					delete(tier, oldCell)
				}
			}
		}
	}

	// Insert into every precision tier.
	for _, p := range s.precisions {
		cell := geohash.EncodeWithPrecision(lat, lng, p)
		tier := s.index[p]
		if tier[cell] == nil {
			tier[cell] = make(map[string]struct{})
		}
		tier[cell][userID] = struct{}{}
	}

	s.points[userID] = entry
	s.Unlock()

	// Write into the LRU/keyVal store so TTL eviction triggers deleteLocationLocal.
	if err := g.cache.setLocal(bucket, userID, entry, ttl); err != nil {
		// Roll back the geo index so it doesn't hold an entry that will never be evicted.
		g.deleteLocationLocal(bucket, userID) //nolint:errcheck
		return err
	}
	return nil
}

// deleteLocationLocal removes a user's location from every precision tier in the
// local geoStore and from the LRU/keyVal store.
func (g *geoCache) deleteLocationLocal(bucket, userID string) error {
	s, err := g.getStore(bucket)
	if err != nil {
		_ = g.cache.deleteLocal(bucket, userID)
		return err
	}

	s.Lock()
	if entry, exists := s.points[userID]; exists {
		for _, p := range s.precisions {
			cell := geohash.EncodeWithPrecision(entry.Lat, entry.Lng, p)
			tier := s.index[p]
			if ids, ok := tier[cell]; ok {
				delete(ids, userID)
				if len(ids) == 0 {
					delete(tier, cell)
				}
			}
		}
		delete(s.points, userID)
	}
	s.Unlock()

	return g.cache.deleteLocal(bucket, userID)
}

// BoundingBox returns all user locations within the given coordinate box,
// querying the index at the specified precision level.
// precision must be one of the levels configured when the bucket was created.
func (g *geoCache) BoundingBox(bucket string, minLat, minLng, maxLat, maxLng float64, precision uint) ([]GeoEntry, error) {
	s, err := g.getStore(bucket)
	if err != nil {
		return nil, err
	}
	if !s.hasPrecision(precision) {
		return nil, ERR_BAD_PRECISION
	}

	cells := coveringCells(minLat, minLng, maxLat, maxLng, precision)

	s.RLock()
	defer s.RUnlock()

	tier := s.index[precision]
	var results []GeoEntry
	seenUsers := make(map[string]struct{})

	for cell := range cells {
		ids, ok := tier[cell]
		if !ok {
			continue
		}
		for userID := range ids {
			if _, already := seenUsers[userID]; already {
				continue
			}
			entry, exists := s.points[userID]
			if !exists {
				continue
			}
			// Exact coordinate filter — geohash cells can straddle the box boundary.
			if entry.Lat >= minLat && entry.Lat <= maxLat &&
				entry.Lng >= minLng && entry.Lng <= maxLng {
				results = append(results, entry)
				seenUsers[userID] = struct{}{}
			}
		}
	}

	return results, nil
}

// RadiusQuery returns all user locations within radiusKm of the given point,
// querying the index at the specified precision level.
// precision must be one of the levels configured when the bucket was created.
func (g *geoCache) RadiusQuery(bucket string, lat, lng float64, radiusKm float64, precision uint) ([]GeoEntry, error) {
	minLat, minLng, maxLat, maxLng := boundingBoxFromRadius(lat, lng, radiusKm)

	candidates, err := g.BoundingBox(bucket, minLat, minLng, maxLat, maxLng, precision)
	if err != nil {
		return nil, err
	}

	var results []GeoEntry
	for _, entry := range candidates {
		if haversineKm(lat, lng, entry.Lat, entry.Lng) <= radiusKm {
			results = append(results, entry)
		}
	}
	return results, nil
}

// coveringCells returns the set of geohash cells at the given precision that
// intersect the bounding box, flood-filling outward from the centre cell.
// Only cells that actually overlap the query box are included in the result.
func coveringCells(minLat, minLng, maxLat, maxLng float64, precision uint) map[string]struct{} {
	centreLat := (minLat + maxLat) / 2
	centreLng := (minLng + maxLng) / 2
	seed := geohash.EncodeWithPrecision(centreLat, centreLng, precision)

	// enqueued tracks every cell we have ever added to the queue so we never
	// enqueue the same cell twice, even before it has been visited.
	enqueued := make(map[string]struct{})
	result := make(map[string]struct{})
	queue := []string{seed}
	enqueued[seed] = struct{}{}

	for len(queue) > 0 {
		cell := queue[0]
		queue = queue[1:]

		cb := geohash.BoundingBox(cell)
		if cb.MaxLat < minLat || cb.MinLat > maxLat ||
			cb.MaxLng < minLng || cb.MinLng > maxLng {
			// Cell does not intersect the box — don't add it to results,
			// but we still stop expansion here so we don't walk past the edge.
			continue
		}

		// Cell intersects: record it and expand to neighbours.
		result[cell] = struct{}{}
		for _, n := range geohash.Neighbors(cell) {
			if _, seen := enqueued[n]; !seen {
				enqueued[n] = struct{}{}
				queue = append(queue, n)
			}
		}
	}

	return result
}

// boundingBoxFromRadius derives a lat/lng bounding box for a circle.
// Near the poles (|lat| > ~89.9°) cosLat approaches zero; the longitude delta
// is clamped so it never becomes infinite, and the box simply spans all longitudes.
func boundingBoxFromRadius(lat, lng, radiusKm float64) (minLat, minLng, maxLat, maxLng float64) {
	const earthRadiusKm = 6371.0
	deltaLat := radiusKm / earthRadiusKm * (180.0 / math.Pi)
	cosLat := math.Cos(lat * math.Pi / 180.0)
	if cosLat < 1e-10 {
		// At or very near a pole — longitude wraps fully around.
		cosLat = 1e-10
	}
	deltaLng := radiusKm / (earthRadiusKm * cosLat) * (180.0 / math.Pi)
	if deltaLng > 180.0 {
		deltaLng = 180.0
	}
	minLat = lat - deltaLat
	maxLat = lat + deltaLat
	minLng = lng - deltaLng
	maxLng = lng + deltaLng
	return
}

// haversineKm returns the great-circle distance in kilometres between two points.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180.0
	dLng := (lng2 - lng1) * math.Pi / 180.0
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180.0)*math.Cos(lat2*math.Pi/180.0)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}
