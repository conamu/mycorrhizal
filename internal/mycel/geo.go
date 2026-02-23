package mycel

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/conamu/mycorrizal/internal/nodosum"
)

var (
	ERR_NO_GEO_BUCKET  = errors.New("geo bucket not found")
	ERR_USER_NOT_FOUND = errors.New("user location not found")
	ERR_BAD_PRECISION  = errors.New("precision not indexed for this bucket")
)

// GeoEntry is the unit returned by spatial queries.
type GeoEntry struct {
	UserID    string
	Lat       float64
	Lng       float64
	Geohash   string // stored at the highest configured precision
	ExpiresAt time.Time
}

// GeoCache is the spatial query interface exposed to library users.
type GeoCache interface {
	// SetLocation stores or updates a user's location in the geo bucket.
	// The write is replicated to all nodes via fire-and-forget broadcast.
	// ttl of 0 uses the bucket default.
	SetLocation(bucket, userID string, lat, lng float64, ttl time.Duration) error

	// DeleteLocation removes a user's location from the geo bucket on all nodes.
	DeleteLocation(bucket, userID string) error

	// BoundingBox returns all user locations within the given coordinate box.
	// precision must be one of the levels configured when the bucket was created.
	// All results are resolved locally — zero network hops.
	BoundingBox(bucket string, minLat, minLng, maxLat, maxLng float64, precision uint) ([]GeoEntry, error)

	// RadiusQuery returns all user locations within radiusKm of the given point.
	// precision must be one of the levels configured when the bucket was created.
	// All results are resolved locally — zero network hops.
	RadiusQuery(bucket string, lat, lng float64, radiusKm float64, precision uint) ([]GeoEntry, error)
}

// geoStore holds the spatial index and point data for a single geo bucket.
// It is fully replicated on every node.
type geoStore struct {
	sync.RWMutex
	// precisions is the sorted slice of indexed precision levels, highest last.
	// The highest value is the canonical precision stored in GeoEntry.Geohash.
	precisions []uint
	// index maps precision → geohash cell → set of userIDs.
	// A separate map tier per precision level allows O(1) lookup at any precision.
	index map[uint]map[string]map[string]struct{}
	// points maps userID → GeoEntry for O(1) point lookups and flush iteration.
	points map[string]GeoEntry
}

// maxPrecision returns the highest configured precision for this store.
// The slice is always sorted ascending so the last element is the max.
func (s *geoStore) maxPrecision() uint {
	return s.precisions[len(s.precisions)-1]
}

// hasPrecision reports whether p is a configured precision level for this store.
func (s *geoStore) hasPrecision(p uint) bool {
	for _, v := range s.precisions {
		if v == p {
			return true
		}
	}
	return false
}

// geoCache is the concrete implementation of GeoCache, attached to cache.
type geoCache struct {
	ctx       context.Context
	logger    *slog.Logger
	app       nodosum.Application
	cache     *cache // back-reference for LRU/TTL integration
	flushFunc GeoFlushFunc
	mu        sync.RWMutex
	stores    map[string]*geoStore // bucket name → store
}

func newGeoCache(ctx context.Context, logger *slog.Logger, app nodosum.Application, c *cache) *geoCache {
	return &geoCache{
		ctx:    ctx,
		logger: logger,
		app:    app,
		cache:  c,
		stores: make(map[string]*geoStore),
	}
}

// createStore registers a new geoStore for a bucket with the given precision levels.
// Called by CreateGeoBucket and on receipt of the first replicated GEO_SET for a bucket.
// Returns ERR_BAD_PRECISION if precisions is empty. Is a no-op if the store already exists.
func (g *geoCache) createStore(bucket string, precisions []uint) error {
	if len(precisions) == 0 {
		return ERR_BAD_PRECISION
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.stores[bucket]; exists {
		return nil
	}

	// Sort precisions ascending so maxPrecision() is always the last element.
	sorted := make([]uint, len(precisions))
	copy(sorted, precisions)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Initialise an index map tier for each precision level.
	index := make(map[uint]map[string]map[string]struct{}, len(sorted))
	for _, p := range sorted {
		index[p] = make(map[string]map[string]struct{})
	}

	g.stores[bucket] = &geoStore{
		precisions: sorted,
		index:      index,
		points:     make(map[string]GeoEntry),
	}
	return nil
}

// getStore returns the geoStore for a bucket, or an error if not found.
func (g *geoCache) getStore(bucket string) (*geoStore, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s, ok := g.stores[bucket]
	if !ok {
		return nil, ERR_NO_GEO_BUCKET
	}
	return s, nil
}
