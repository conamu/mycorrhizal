package mycel

import "github.com/conamu/go-worker"

type rebalancer struct{}

func newRebalancer() *rebalancer {
	return &rebalancer{}
}

func (r *rebalancer) start() {}

func (r *rebalancer) stop() {}

func (r *rebalancer) rebalancerWorkerTask(w *worker.Worker, msg any) {}
