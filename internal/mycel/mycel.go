package mycel

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/conamu/go-worker"
	"github.com/conamu/mycorrizal/internal/nodosum"
)

type Mycel interface {
	Start() error
	Ready(timeout time.Duration) error
	Shutdown()
	Cache() Cache
}

type mycel struct {
	ctx          context.Context
	cancel       context.CancelFunc
	wg           *sync.WaitGroup
	logger       *slog.Logger
	ndsm         *nodosum.Nodosum
	app          nodosum.Application
	readyChan    chan any
	cache        *cache
	replicaCount int
}

/*

Mycel is built with 2 types of caches in mind

1. Standard cache with LRU eviction + instant eventual persistence in file-based s3 storage.

2. Fast Access cache with LRU and TTL based eviction

both types are fully replicated on all nodes

A leader is elected with a simple algorithm, including healthchecks and rapid new election.
Every node has a WAL so no records are missed in case leader is down for a short period

Leader is responsible for managing the Distributed linked list structure

Leader:
- Double linked list with record ids and origin node id in case data is not yet synced across the cluster
	- sends signal to evict record id

- Hashmap to map to records -> can be used for partial replication of data, saving memory in the future
- Partial replication algorithm can be based on bucket hashing or a trie data structure.

Nodes:
- Hold data with record ids
- Access data
- Control of data tied to leader

*/

func New(cfg *Config) (Mycel, error) {
	ctx, cancel := context.WithCancel(cfg.Ctx)

	m := &mycel{
		ctx:       ctx,
		cancel:    cancel,
		wg:        &sync.WaitGroup{},
		logger:    cfg.Logger,
		ndsm:      cfg.Nodosum,
		readyChan: make(chan any),
	}

	m.app = m.ndsm.RegisterApplication("SYSTEM-MYCEL")
	m.initCache()

	m.app.SetReceiveFunc(m.cache.applicationReceiveFunc)
	m.app.SetRequestHandler(m.cache.applicationRequestHandlerFunc)

	return m, nil
}

func (m *mycel) Start() error {
	m.logger.Debug("mycel waiting on nodosum to be ready...")
	err := m.ndsm.Ready(time.Second * 30)
	if err != nil {
		m.cancel()
		return errors.Join(errors.New("failed to initialize mycel"), err)
	}
	m.logger.Debug("starting mycel")

	go worker.NewWorker(m.ctx, "tt-l-evictor", m.wg, m.cache.ttlEvictionWorkerTask, m.logger, 10*time.Second).Start()
	close(m.readyChan)
	return nil
}

func (m *mycel) Ready(timeout time.Duration) error {
	t := time.NewTimer(timeout)
	for {
		select {
		case <-m.readyChan:
			m.logger.Debug("mycel ready")
			return nil
		case <-t.C:
			return errors.New("mycel did not send ready signal before timeout")
		case <-m.ctx.Done():
			return errors.New("context canceled")
		}
	}
}

func (m *mycel) Shutdown() {
	m.logger.Debug("mycel shutdown")
	m.cancel()
	m.logger.Debug("mycel waiting on routines to exit...")
	m.wg.Wait()
}

func (m *mycel) Cache() Cache {
	return m.cache
}
