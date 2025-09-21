package nodosum

import "sync"

/*

SCOPE

- Discover Instances via Consul API/DNS-SD
- Establish Connections in Star Network Topology (all nodes have a connection to all nodes)
- Manage connections and keep them up
- Provide communication interface to abstract away the cluster
  (this should feel like one big App, even though it could be spread on 10 nodes/instances)
- Provide Interface to create, read, update and delete cluster store resources
  and find/set their location on the cluster.
- Authenticate and Encrypt all Intra-Cluster Communication

*/

type Nodosum struct {
	registry nodeRegistry
}

type nodeRegistry struct {
	nodes map[string]node
	lock  *sync.Mutex
}

type node struct{}

func New(cfg *Config) *Nodosum {
	return &Nodosum{}
}
