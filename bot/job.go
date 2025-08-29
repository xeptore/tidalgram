package bot

import (
	"context"
	"errors"

	"golang.org/x/sync/semaphore"
)

var ErrJobCanceled = errors.New("job canceled")

type Worker struct {
	sem    *semaphore.Weighted
	cancel context.CancelFunc
}

func NewWorker(maxConcurrency int) *Worker {
	return &Worker{
		sem:    semaphore.NewWeighted(int64(maxConcurrency)),
		cancel: func() {},
	}
}

func (w *Worker) TryAcquireJob(ctx context.Context) (context.Context, bool) {
	ok := w.sem.TryAcquire(1)
	if !ok {
		return nil, false
	}

	ctx, cancel := context.WithCancelCause(ctx)
	w.cancel = func() {
		cancel(ErrJobCanceled)
		w.sem.Release(1)
	}

	return ctx, true
}

func (w *Worker) CancelJob() {
	w.cancel()
}
