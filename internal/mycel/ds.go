package mycel

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

/*
Data structure Design for leaderless distributes linked list based LRU and TTL Cache

Basic doubly linked list - every node has its own dll based lru list and evicts accordingly.

If a values hash indicates remote data, it is fetched.

Every node has a minimum of 3 data supplies, best is 5. If one node returns dead from nodosum, the next one is called.

This requires at least 5 nodes to work flawlessly, minimum of 3.

If data available locally -> get data, return, push to top

if data remote:
 - request data from owner node (this node has the original copy and is guaranteed to be up to date)
 	- if nodosum responds with node being dead, use the replicaNode list to fetch the data.
 		- node will need to flag new ownerNode from top of replicaNodes array and notify the cluster of that change.
	- when remote data is accessed the remote node will push it to the top of the list





*/

// Local dll based cache with ttls

type cacheBucket struct {
	sync.Mutex
	head *node
	tail *node
	len  int
}

type node struct {
	id   string
	next *node
	prev *node
	data any
	ttl  time.Time
}

/*
	Rendezvous hashing functions for deterministic cache node selection and fallback
*/

type RendezvousHash struct {
	sync.Mutex
	nodes []hashNode
}

type hashNode struct {
	id    string
	score uint64
}

func (*RendezvousHash) hashScore(key, nodeId string) uint64 {
	h := sha256.New()
	h.Write([]byte(key))
	h.Write([]byte(nodeId))
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint64(sum[:8])
}
