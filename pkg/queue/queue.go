package queue

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// Item represents an item in the work queue
type Item struct {
	Key string
	UID string
}

// Reconciler is the interface that must be implemented by handlers that want to use the queue wrapper
type Reconciler interface {
	Reconcile(ctx context.Context, item Item) error
}

// Handler wraps a Reconciler with a work queue and concurrent workers
type Handler struct {
	reconciler Reconciler
	logger     logr.Logger
	indexer    cache.Indexer
	queue      workqueue.TypedRateLimitingInterface[Item]
	workers    int
	waitErrs   []error
}

var _ cache.ResourceEventHandler = (*Handler)(nil)

// NewHandler creates a new queued event handler that wraps a reconciler
func NewHandler(reconciler Reconciler, indexer cache.Indexer, logger logr.Logger, workers int, waitErrs []error) *Handler {
	if workers <= 0 {
		workers = 1
	}

	return &Handler{
		reconciler: reconciler,
		logger:     logger,
		indexer:    indexer,
		queue:      workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Item]()),
		workers:    workers,
		waitErrs:   waitErrs,
	}
}

// OnAdd handles add events by enqueueing the item
func (q *Handler) OnAdd(obj interface{}, _ bool) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}

	uid := ""
	if service, ok := obj.(*corev1.Service); ok {
		uid = string(service.UID)
	}

	q.queue.Add(Item{Key: key, UID: uid})
}

// OnUpdate handles update events by enqueueing the item
func (q *Handler) OnUpdate(_, newObj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(newObj)
	if err != nil {
		return
	}

	uid := ""
	if service, ok := newObj.(*corev1.Service); ok {
		uid = string(service.UID)
	}

	q.queue.Add(Item{Key: key, UID: uid})
}

// OnDelete handles delete events by enqueueing the item
func (q *Handler) OnDelete(obj interface{}) {
	// extract the object from tombstone if needed
	var service *corev1.Service
	var ok bool

	if service, ok = obj.(*corev1.Service); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			q.logger.Info("error decoding object, invalid type")
			return
		}
		service, ok = tombstone.Obj.(*corev1.Service)
		if !ok {
			q.logger.Info("error decoding object tombstone, invalid type")
			return
		}
	}

	key, err := cache.MetaNamespaceKeyFunc(service)
	if err != nil {
		return
	}

	// enqueue with UID. Let Reconcile() determine if it's really deleted by checking the indexer
	q.queue.Add(Item{Key: key, UID: string(service.UID)})
}

// Start launches the worker goroutines and blocks until the context is done
func (q *Handler) Start(ctx context.Context) {
	defer q.queue.ShutDown()

	q.logger.Info("starting workers", "count", q.workers)

	for i := 0; i < q.workers; i++ {
		go wait.UntilWithContext(ctx, q.runWorker, time.Second)
	}

	<-ctx.Done()
	q.logger.Info("stopping workers")
}

func (q *Handler) runWorker(ctx context.Context) {
	for q.processNextWorkItem(ctx) {
	}
}

func (q *Handler) processNextWorkItem(ctx context.Context) bool {
	item, quit := q.queue.Get()
	if quit {
		return false
	}
	defer q.queue.Done(item)

	err := q.reconciler.Reconcile(ctx, item)
	q.handleErr(err, item)

	return true
}

func (q *Handler) handleErr(err error, item Item) {
	if err == nil {
		q.queue.Forget(item)
		return
	}

	// check if this is a wait error (resources not ready yet)
	isWaiting := false
	for _, waitErr := range q.waitErrs {
		if waitErr != nil && errors.Is(err, waitErr) {
			isWaiting = true
			break
		}
	}

	if isWaiting {
		// log at info level - this is an expected condition during normal operation
		q.logger.Info(err.Error()+", waiting...", "key", item.Key)
	} else {
		// log actual errors at error level with stack trace for debugging
		q.logger.Error(err, "error reconciling item, will retry", "key", item.Key)
	}
	q.queue.AddRateLimited(item)
}
