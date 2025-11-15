package mycel

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"sync"
	"time"
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

func (m *mycel) initCache() {
	m.cache = &cache{
		keyVal:           &keyVal{data: make(map[string]*node)},
		lruBuckets:       &lruBuckets{data: make(map[string]*lruBucket)},
		nodeScoreHashMap: &remoteCacheNodeHashMap{data: make(map[string]string)},
	}
}

// cache holds references to all nodes and buckets protected by mu
type cache struct {
	sync.WaitGroup
	keyVal           *keyVal
	lruBuckets       *lruBuckets
	nodeScoreHashMap *remoteCacheNodeHashMap
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
	sync.Mutex
	head   *node
	tail   *node
	len    int
	maxLen int
}

type node struct {
	sync.RWMutex
	next *node
	prev *node
	hash string
	data any
	ttl  time.Time
}

/*
	Rendezvous hashing functions for deterministic cache node selection and fallback
*/

type replicaNode struct {
	id    string
	score uint64
}

// score calculates the score of a node selection for a cache key. Data is written to N nodes with highest scores
func (m *mycel) score(key, nodeId string) uint64 {
	h := sha256.New()
	h.Write([]byte(key))
	h.Write([]byte(nodeId))
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint64(sum[:8])
}

// getReplicas calculates N possible replicas for cache key. Returned slice is sorted by highest score.
func (m *mycel) getReplicas(key string, replicas int) []replicaNode {
	nodes := m.app.Nodes()
	var scoredNodes []replicaNode

	for i, nodeId := range nodes {
		if i >= replicas {
			break
		}
		nodeScore := m.score(key, nodeId)
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

func (l *lruBuckets) GetBucket(name string) *lruBucket {
	l.RLock()
	defer l.RUnlock()
	return l.data[name]
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

	// if item was between 2 nodes, stitch them together before moving it to the top
	if n.next != nil && n.prev != nil {
		prev := n.prev
		next := n.next

		next.prev = prev
		prev.next = next
	}

	if l.head == nil {
		l.head = n
		l.tail = n
	}

	if l.head != nil {
		l.head.prev = n
		n.next = l.head
		l.head = n
	}
	n.prev = nil
	l.len++
}

// keyVal methods

func (k *keyVal) Get(key string) *node {
	k.RLock()
	defer k.RUnlock()
	return k.data[key]
}
