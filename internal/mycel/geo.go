package mycel

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/conamu/mycorrizal/internal/nodosum"
)

var (
	ERR_NO_GEO_BUCKET = errors.New("geo bucket not found")
	ERR_USER_NOT_FOUND = errors.New("user location not found")
)

// GeoEntry is the unit returned by spatial queries.
type GeoEntry struct {
	UserID    string
	Lat       float64
	Lng       float64
	Geohash   string
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
	// All results are resolved locally — zero network hops.
	BoundingBox(bucket string, minLat, minLng, maxLat, maxLng float64) ([]GeoEntry, error)

	// RadiusQuery returns all user locations within radiusKm of the given point.
	// All results are resolved locally — zero network hops.
	RadiusQuery(bucket string, lat, lng float64, radiusKm float64) ([]GeoEntry, error)
}

// geoStore holds the spatial index and point data for a single geo bucket.
// It is fully replicated on every node.
type geoStore struct {
	sync.RWMutex
	// index maps geohash cell (at full precision) → set of userIDs
	index map[string]map[string]struct{}
	// points maps userID → GeoEntry for O(1) lookups and flush iteration
	points map[string]GeoEntry
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

// createStore registers a new geoStore for a bucket. Called by CreateGeoBucket.
func (g *geoCache) createStore(bucket string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.stores[bucket]; !exists {
		g.stores[bucket] = &geoStore{
			index:  make(map[string]map[string]struct{}),
			points: make(map[string]GeoEntry),
		}
	}
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
