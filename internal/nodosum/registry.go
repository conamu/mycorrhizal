package nodosum

import "sync"

type nodeRegistry struct {
	nodes map[string]node
	lock  *sync.Mutex
}

func newNodeRegistry() *nodeRegistry {
	return &nodeRegistry{
		nodes: make(map[string]node),
		lock:  new(sync.Mutex),
	}
}

type node struct{}
