package mycel

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

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
	GET      uint8 = 0x00
	SET      uint8 = 0x01
	SETTTL   uint8 = 0x02
	DELETE   uint8 = 0x03
	RESPONSE uint8 = 0x04
)

func (m *mycel) initCache() {
	m.cache = &cache{
		ctx:              m.ctx,
		logger:           m.logger,
		meter:            m.meter,
		nodeId:           m.ndsm.Id(),
		app:              m.app,
		keyVal:           &keyVal{data: make(map[string]*node)},
		lruBuckets:       &lruBuckets{data: make(map[string]*lruBucket)},
		nodeScoreHashMap: &remoteCacheNodeHashMap{data: make(map[string]string)},
		replicas:         m.replicas,
		remoteTimeout:    m.remoteTimeout,
	}
}

// cache holds references to all nodes and buckets protected by mu
type cache struct {
	sync.WaitGroup
	ctx              context.Context
	logger           *slog.Logger
	meter            metric.Meter
	nodeId           string
	app              nodosum.Application
	keyVal           *keyVal
	lruBuckets       *lruBuckets
	nodeScoreHashMap *remoteCacheNodeHashMap
	replicas         int
	remoteTimeout    time.Duration
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

// lruBucket is a local dll based cache with ttls
type lruBucket struct {
	sync.RWMutex
	head       *node
	tail       *node
	len        int
	maxLen     int
	defaultTtl time.Duration
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

// score calculates the score of a node selection for a cache key. Data is written to N nodes with highest scores
func (c *cache) score(key, nodeId string) uint64 {
	h := sha256.New()
	h.Write([]byte(key))
	h.Write([]byte(nodeId))
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint64(sum[:8])
}

// getReplicas calculates replicas scores for cache key. Returned slice is sorted by highest score.
func (c *cache) getReplicas(key string) []replicaNode {
	nodes := c.app.Nodes()
	nodes = append(nodes, c.nodeId)
	var scoredNodes []replicaNode

	for _, nodeId := range nodes {
		nodeScore := c.score(key, nodeId)
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

// CreateBucket creates a bucket with name and set ttl and a configured max LRU length
func (l *lruBuckets) CreateBucket(name string, defaultTtl time.Duration, maxLen int) (*lruBucket, error) {
	l.Lock()
	defer l.Unlock()

	if l.data[name] != nil {
		return nil, errors.New("bucket already exists")
	}

	l.data[name] = &lruBucket{
		maxLen:     maxLen,
		defaultTtl: defaultTtl,
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
