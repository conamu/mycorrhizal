package mycel

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/conamu/mycorrizal/internal/nodosum"
	"go.opentelemetry.io/otel/metric"
)

/*
Data structure Design for leaderless distributed linked list based LRU and TTL Cache

Every node has its own Doubly Linked List based LRU Cache and evicts accordingly.

With the cache key and node ids the correct primary nodes and N backups are calculated via rendezvous caching.

Local Processes:

Data Retrieval:
- Entry is pushed to top of Cache Bucket LRU List

Data Entry:
- new cache node is created and put to top of LRU List
- TTL is set to default or according to input

Other allowed interactions:
- Data Deletion: remove from map and from LRU List
- Update: Push to top
- Set TTL: Push to Top and update TTL

*/

const (
	GET     uint8 = 0x00
	SET     uint8 = 0x01
	SETTTL  uint8 = 0x02
	DELETE  uint8 = 0x03
	RESPONSE uint8 = 0x04
	GEO_SET uint8 = 0x05
)

// bucketType distinguishes regular rendezvous-distributed buckets from
// fully-replicated geo buckets.
type bucketType int

const (
	bucketTypeRegular bucketType = iota
	bucketTypeGeo
)

func (m *mycel) initCache() {
	mt, err := newMetrics(m.meter)
	if err != nil {
		m.logger.Warn("failed to initialize cache metrics, OTEL may not be configured", "error", err)
	}

	m.cache = &cache{
		ctx:                  m.ctx,
		logger:               m.logger,
		meter:                m.meter,
		metrics:              mt,
		nodeId:               m.ndsm.Id(),
		app:                  m.app,
		keyVal:               &keyVal{data: make(map[string]*node)},
		lruBuckets:           &lruBuckets{data: make(map[string]*lruBucket)},
		nodeScoreHashMap:     &remoteCacheNodeHashMap{data: make(map[string]string)},
		replicaLocalityCache: &replicaLocalityCache{data: make(map[string]bool)},
		replicas:             m.replicas,
		remoteTimeout:        m.remoteTimeout,
	}

	m.cache.geo = newGeoCache(m.ctx, m.logger, m.app, m.cache)
}

// cache holds references to all nodes and buckets protected by mu
type cache struct {
	sync.WaitGroup
	ctx                  context.Context
	logger               *slog.Logger
	meter                metric.Meter
	metrics              *metrics
	nodeId               string
	app                  nodosum.Application
	keyVal               *keyVal
	lruBuckets           *lruBuckets
	nodeScoreHashMap     *remoteCacheNodeHashMap
	replicaLocalityCache *replicaLocalityCache
	geo                  *geoCache
	replicas             int
	remoteTimeout        time.Duration
}

type metrics struct {
	gets         metric.Int64Counter
	sets         metric.Int64Counter
	deletes      metric.Int64Counter
	ttlUpdates   metric.Int64Counter
	hits         metric.Int64Counter
	misses       metric.Int64Counter
	lruEvictions metric.Int64Counter
	ttlEvictions metric.Int64Counter
	bucketSize   metric.Int64UpDownCounter
	duration     metric.Int64Histogram
	errors       metric.Int64Counter
}

type keyVal struct {
	sync.RWMutex
	data map[string]*node
}

type lruBuckets struct {
	sync.RWMutex
	data map[string]*lruBucket
}

// remoteCacheNodeHashMap stores references to the primary replica of the cache
// key to avoid calculation of primary node on every read
type remoteCacheNodeHashMap struct {
	sync.RWMutex
	data map[string]string
}

// replicaLocalityCache caches whether this node is among the top-N replicas for a key.
// Invalidated on topology changes. Avoids repeated getReplicas() calls on the hot path.
type replicaLocalityCache struct {
	sync.RWMutex
	data map[string]bool
}

// lruBucket is a local dll based cache with ttls
type lruBucket struct {
	sync.RWMutex
	bType           bucketType // bucketTypeRegular or bucketTypeGeo
	geoRoutingPrec  uint       // lowest configured precision, used for FNV-1a rendezvous routing (geo buckets only)
	head            *node
	tail            *node
	len             int
	maxLen          int
	defaultTtl      time.Duration
}

type node struct {
	sync.RWMutex
	next      *node
	prev      *node
	key       string
	data      any
	expiresAt time.Time
}

/*
	Rendezvous hashing functions for deterministic cache node selection and fallback
*/

type replicaNode struct {
	id    string
	score uint64
}

// scoreRegular scores a regular cache key against a node using xxHash64.
// High avalanche property ensures uniform, input-independent distribution.
func (c *cache) scoreRegular(key, nodeId string) uint64 {
	h := xxhash.New()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte(nodeId))
	return h.Sum64()
}

// scoreGeo scores a geohash prefix against a node using FNV-1a 64-bit.
// Low avalanche property preserves the spatial structure of the geohash prefix,
// so adjacent cells tend to score similarly and land on the same node.
func (c *cache) scoreGeo(geohashPrefix, nodeId string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(geohashPrefix))
	_, _ = h.Write([]byte(nodeId))
	return h.Sum64()
}

// getReplicas calculates rendezvous scores for a cache key across all known nodes.
// For geo buckets the key is truncated to the bucket's geoPrecision before scoring.
// The returned slice is sorted by score descending; index 0 is the primary node.
func (c *cache) getReplicas(key string) []replicaNode {
	nodes := c.app.Nodes()
	nodes = append(nodes, c.nodeId)
	var scoredNodes []replicaNode

	// Determine which scorer and key form to use based on bucket type.
	// We look up the bucket by checking whether the key starts with a known geo bucket prefix.
	// The bucket name is embedded in the composite key passed by isLocal (bucket+key),
	// so we pass the full composite key and each scorer uses it as-is.
	// For geo buckets the caller (isLocal) already passes the truncated routing key.
	for _, nodeId := range nodes {
		nodeScore := c.scoreRegular(key, nodeId)
		scoredNodes = append(scoredNodes, replicaNode{
			id:    nodeId,
			score: nodeScore,
		})
	}

	sort.Slice(scoredNodes, func(i, j int) bool {
		return scoredNodes[i].score > scoredNodes[j].score
	})

	return scoredNodes
}

// getReplicasGeo calculates rendezvous scores using the FNV-1a geo scorer.
// geohashPrefix should already be truncated to the bucket's routing precision.
func (c *cache) getReplicasGeo(geohashPrefix string) []replicaNode {
	nodes := c.app.Nodes()
	nodes = append(nodes, c.nodeId)
	var scoredNodes []replicaNode

	for _, nodeId := range nodes {
		nodeScore := c.scoreGeo(geohashPrefix, nodeId)
		scoredNodes = append(scoredNodes, replicaNode{
			id:    nodeId,
			score: nodeScore,
		})
	}

	sort.Slice(scoredNodes, func(i, j int) bool {
		return scoredNodes[i].score > scoredNodes[j].score
	})

	return scoredNodes
}

// LRU DLL Methods

// GetBucket returns a bucket by name
func (l *lruBuckets) GetBucket(name string) (*lruBucket, error) {
	l.RLock()
	defer l.RUnlock()
	b := l.data[name]
	if b == nil {
		return nil, errors.New("bucket not found")
	}

	return b, nil
}

// CreateBucket creates a regular rendezvous-distributed bucket.
func (l *lruBuckets) CreateBucket(name string, defaultTtl time.Duration, maxLen int) (*lruBucket, error) {
	l.Lock()
	defer l.Unlock()

	if l.data[name] != nil {
		return nil, errors.New("bucket already exists")
	}

	l.data[name] = &lruBucket{
		bType:      bucketTypeRegular,
		maxLen:     maxLen,
		defaultTtl: defaultTtl,
	}

	return l.data[name], nil
}

// CreateGeoBucketInternal creates a fully-replicated geo bucket.
// precisions is the set of geohash precision levels to index; the lowest value
// is used as the FNV-1a rendezvous routing key truncation length.
func (l *lruBuckets) CreateGeoBucketInternal(name string, defaultTtl time.Duration, maxLen int, precisions []uint) (*lruBucket, error) {
	l.Lock()
	defer l.Unlock()

	if l.data[name] != nil {
		return nil, errors.New("bucket already exists")
	}

	// Use the lowest precision as the routing precision for FNV-1a scoring.
	routingPrec := precisions[0]
	for _, p := range precisions {
		if p < routingPrec {
			routingPrec = p
		}
	}

	l.data[name] = &lruBucket{
		bType:          bucketTypeGeo,
		geoRoutingPrec: routingPrec,
		maxLen:         maxLen,
		defaultTtl:     defaultTtl,
	}

	return l.data[name], nil
}

// Push appends or moves the node to the top of the list
func (l *lruBucket) Push(n *node) {
	// Since this operation concerns the whole bucket we dont need to lock the individual node but still doing it to be sure.
	l.Lock()
	n.Lock()
	defer l.Unlock()
	defer n.Unlock()

	if l.head == n {
		return
	}

	// If node never had neighbours its new
	if n.next == nil && n.prev == nil {
		l.len++
	}

	// if item was between 2 nodes, stitch them together before moving it to the top
	if n.next != nil && n.prev != nil {
		next := n.next
		prev := n.prev

		next.prev = prev
		prev.next = next
	}

	// if item is the tail (but not the only node), update tail pointer
	if n.next == nil && n.prev != nil {
		l.tail = n.prev
		n.prev.next = nil
	}

	if l.head == nil {
		l.head = n
		l.tail = n
		return
	}

	if l.head != nil {
		l.head.prev = n
		n.next = l.head
		l.head = n
	}
	n.prev = nil
}

func (l *lruBucket) Delete(key string) {
	l.Lock()
	defer l.Unlock()

	n := l.head
	for n != nil {
		n.RLock()
		if n.key == key {
			// Update head if this is the head node
			if n.prev == nil {
				l.head = n.next
			}
			// Update tail if this is the tail node
			if n.next == nil {
				l.tail = n.prev
			}

			// Stitch together nodes if this is a middle node
			if n.next != nil && n.prev != nil {
				prev := n.prev
				next := n.next

				next.prev = prev
				prev.next = next
			}

			// Clear pointers for head-only node (not single node)
			if n.prev == nil && n.next != nil {
				n.next.prev = nil
			}

			// Clear pointers for tail-only node (not single node)
			if n.next == nil && n.prev != nil {
				n.prev.next = nil
			}

			// Clear the deleted nodes own pointers
			n.RUnlock()
			n.Lock()
			n.next = nil
			n.prev = nil
			n.data = nil
			n.Unlock()

			l.len--
			return
		}
		n.RUnlock()
		n = n.next
	}

}

// keyVal methods

func (k *keyVal) Get(key string) *node {
	k.RLock()
	defer k.RUnlock()
	return k.data[key]
}

func (k *keyVal) Set(key string, n *node) {
	k.Lock()
	defer k.Unlock()
	k.data[key] = n
}

func (k *keyVal) Delete(key string) {
	k.Lock()
	defer k.Unlock()
	delete(k.data, key)
}
