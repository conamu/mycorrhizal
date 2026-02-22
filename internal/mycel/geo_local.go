package mycel

import (
	"math"
	"time"

	"github.com/mmcloughlin/geohash"
)

// geohashPrecision is the full precision used when encoding user locations.
// At precision 9 each cell is ~4m×4m — sufficient for user location granularity.
const geohashFullPrecision = 9

// setLocationLocal writes a user's location into the local geoStore and the
// regular LRU/keyVal store so TTL eviction works for free via the existing worker.
func (g *geoCache) setLocationLocal(bucket, userID string, lat, lng float64, ttl time.Duration) error {
	s, err := g.getStore(bucket)
	if err != nil {
		return err
	}

	cell := geohash.EncodeWithPrecision(lat, lng, geohashFullPrecision)

	entry := GeoEntry{
		UserID:  userID,
		Lat:     lat,
		Lng:     lng,
		Geohash: cell,
	}

	s.Lock()
	// Remove the user from their old cell index entry if they moved.
	if old, exists := s.points[userID]; exists && old.Geohash != cell {
		if ids, ok := s.index[old.Geohash]; ok {
			delete(ids, userID)
			if len(ids) == 0 {
				delete(s.index, old.Geohash)
			}
		}
	}

	// Add to new cell index.
	if s.index[cell] == nil {
		s.index[cell] = make(map[string]struct{})
	}
	s.index[cell][userID] = struct{}{}
	s.points[userID] = entry
	s.Unlock()

	// Also write into the LRU/keyVal store under bucket+userID so the existing
	// TTL eviction worker handles expiry. On eviction deleteLocal is called which
	// will remove the entry from the geoStore via deleteLocationLocal.
	return g.cache.setLocal(bucket, userID, entry, ttl)
}

// deleteLocationLocal removes a user's location from the local geoStore and LRU.
func (g *geoCache) deleteLocationLocal(bucket, userID string) error {
	s, err := g.getStore(bucket)
	if err != nil {
		// If the geo store doesn't exist we still attempt the LRU delete.
		_ = g.cache.deleteLocal(bucket, userID)
		return err
	}

	s.Lock()
	if entry, exists := s.points[userID]; exists {
		if ids, ok := s.index[entry.Geohash]; ok {
			delete(ids, userID)
			if len(ids) == 0 {
				delete(s.index, entry.Geohash)
			}
		}
		delete(s.points, userID)
	}
	s.Unlock()

	return g.cache.deleteLocal(bucket, userID)
}

// BoundingBox returns all user locations within the given coordinate box.
// Resolution is performed entirely against the local replicated index.
func (g *geoCache) BoundingBox(bucket string, minLat, minLng, maxLat, maxLng float64) ([]GeoEntry, error) {
	s, err := g.getStore(bucket)
	if err != nil {
		return nil, err
	}

	// Collect all geohash cells that cover the bounding box by flood-filling
	// from the centre cell outward using Neighbors until we leave the box.
	cells := coveringCells(minLat, minLng, maxLat, maxLng)

	s.RLock()
	defer s.RUnlock()

	var results []GeoEntry
	seenUsers := make(map[string]struct{})

	for cell := range cells {
		ids, ok := s.index[cell]
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

// coveringCells returns the set of geohash cells at geohashFullPrecision that
// cover the given bounding box. It flood-fills from a seed cell (the box centre)
// using Neighbors, stopping when the cell's bounding box no longer overlaps.
func coveringCells(minLat, minLng, maxLat, maxLng float64) map[string]struct{} {
	centreLat := (minLat + maxLat) / 2
	centreLng := (minLng + maxLng) / 2
	seed := geohash.EncodeWithPrecision(centreLat, centreLng, geohashFullPrecision)

	visited := make(map[string]struct{})
	queue := []string{seed}

	for len(queue) > 0 {
		cell := queue[0]
		queue = queue[1:]

		if _, seen := visited[cell]; seen {
			continue
		}
		visited[cell] = struct{}{}

		// Check if this cell's bounding box overlaps the query box.
		cb := geohash.BoundingBox(cell)
		if cb.MaxLat < minLat || cb.MinLat > maxLat ||
			cb.MaxLng < minLng || cb.MinLng > maxLng {
			continue
		}

		for _, n := range geohash.Neighbors(cell) {
			if _, seen := visited[n]; !seen {
				queue = append(queue, n)
			}
		}
	}

	return visited
}

// RadiusQuery returns all user locations within radiusKm of the given point.
// It derives a bounding box from the radius, delegates to BoundingBox for
// candidate retrieval, then filters by exact Haversine distance.
func (g *geoCache) RadiusQuery(bucket string, lat, lng float64, radiusKm float64) ([]GeoEntry, error) {
	minLat, minLng, maxLat, maxLng := boundingBoxFromRadius(lat, lng, radiusKm)

	candidates, err := g.BoundingBox(bucket, minLat, minLng, maxLat, maxLng)
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

// boundingBoxFromRadius derives a lat/lng bounding box for a circle defined by
// a centre point and radius in kilometres.
func boundingBoxFromRadius(lat, lng, radiusKm float64) (minLat, minLng, maxLat, maxLng float64) {
	// Earth's radius in km.
	const earthRadiusKm = 6371.0

	deltaLat := radiusKm / earthRadiusKm * (180.0 / math.Pi)
	deltaLng := radiusKm / (earthRadiusKm * math.Cos(lat*math.Pi/180.0)) * (180.0 / math.Pi)

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
