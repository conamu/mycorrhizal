package mycel

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/conamu/go-worker"
	"github.com/conamu/mycorrhizal/internal/nodosum"
	"go.opentelemetry.io/otel/metric"
)

type Mycel interface {
	Start() error
	Ready(timeout time.Duration) error
	Shutdown()
	Cache() Cache
	// Geo returns the GeoCache interface for spatial location operations.
	Geo() GeoCache
}

type mycel struct {
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 *sync.WaitGroup
	logger             *slog.Logger
	ndsm               *nodosum.Nodosum
	meter              metric.Meter
	app                nodosum.Application
	readyChan          chan any
	cache              *cache
	replicas           int
	remoteTimeout      time.Duration
	rebalancer         *rebalancer
	rebalancerInterval time.Duration
	geoFlushInterval   time.Duration
	geoFlushFunc       GeoFlushFunc
}

/*

Mycel is built to be a simple distributed caching and storage layer with eventual persistence.
its API supports Get, Set, Delete, and SetTTL.

The cache has 3 main functionalities:
LRU eviction, TTL eviction, eventual persistence on S3 based storage.

Nodes have their own local ephemeral in memory storage.
This is organized in Buckets. Every bucket has its own LRU and TTL configuration.
Single items can have differing TTL configurations as well.

If a bucket is set as persisted, all data  will be stored.
The Bucket will hold any data according to its LRU and TTL configuration.
If data ways evicted it can be looked up from the s3 based storage

The data is replicated across a minimum of 3 nodes. Owner and backup nodes are calculated by rendezvous hashing.

All data lives locally on the nodes, organized in buckets. LRUs and TTLs are Node scoped.
Data of one bucket can exist on many different nodes.
Buckets are logical constraints for access control and organization.

If a key can not be found locally, the owner node is calculated and a request to retrieve the keys value is sent.
If it times out, the backup nodes are requested.
If this happens too often or the owner node is already marked dead from memberlist
the new owner is calculated and the keys rebalance across the cluster with new backup nodes.

storage and transfer encoding is gob.

Local data is accessed though direct pointers, only transfers and storage gets gob encoding.

Explore using S3 for replication/data log in comparison to directly transferring all data to a new node.

*/

func New(cfg *Config) (Mycel, error) {
	if cfg.Replicas < 1 {
		return nil, errors.New("mycel: Replicas must be >= 1")
	}

	ctx, cancel := context.WithCancel(cfg.Ctx)

	flushInterval := cfg.GeoFlushInterval
	if flushInterval == 0 {
		flushInterval = 30 * time.Second
	}

	m := &mycel{
		ctx:                ctx,
		cancel:             cancel,
		wg:                 &sync.WaitGroup{},
		logger:             cfg.Logger,
		meter:              cfg.Meter,
		ndsm:               cfg.Nodosum,
		readyChan:          make(chan any),
		replicas:           cfg.Replicas,
		remoteTimeout:      cfg.RemoteTimeout,
		rebalancerInterval: cfg.RebalancerInterval,
		geoFlushInterval:   flushInterval,
		geoFlushFunc:       cfg.GeoFlushFunc,
	}

	m.app = m.ndsm.RegisterApplication("SYSTEM-MYCEL")
	m.initCache()

	m.app.SetReceiveFunc(m.cache.applicationReceiveFunc)
	m.app.SetRequestHandler(m.cache.applicationRequestHandlerFunc)

	m.rebalancer = newRebalancer(m.cache, m.wg, m.logger, m.rebalancerInterval)

	// Register topology change hook: invalidate routing caches and trigger rebalancer
	// whenever a node joins or leaves the cluster.
	m.ndsm.SetTopologyChangeHook(func(nodeId string, joined bool) {
		m.cache.invalidateCaches()
		m.rebalancer.triggerImmediate()
	})

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
	m.rebalancer.start()

	if m.geoFlushFunc != nil {
		m.cache.geo.flushFunc = m.geoFlushFunc
		go worker.NewWorker(m.ctx, "geo-flusher", m.wg, m.cache.geo.flushWorkerTask, m.logger, m.geoFlushInterval).Start()
	}

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

func (m *mycel) Geo() GeoCache {
	return m.cache.geo
}
