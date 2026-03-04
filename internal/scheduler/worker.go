package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/storage"
)

// WorkerConfig configures a worker instance.
type WorkerConfig struct {
	Capabilities []string
	Concurrency  int
	PollInterval time.Duration
}

// Worker claims and executes tasks from the queue.
type Worker struct {
	id           string
	store        storage.Repository
	handlers     map[string]TaskHandler
	capabilities []string
	concurrency  int
	pollInterval time.Duration
	bus          *eventbus.Bus
	logger       *zap.Logger
}

// NewWorker creates a new task worker.
func NewWorker(store storage.Repository, cfg WorkerConfig, logger *zap.Logger) *Worker {
	hostname, _ := os.Hostname()
	id := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}

	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}

	return &Worker{
		id:           id,
		store:        store,
		handlers:     make(map[string]TaskHandler),
		capabilities: cfg.Capabilities,
		concurrency:  concurrency,
		pollInterval: pollInterval,
		logger:       logger,
	}
}

// SetEventBus attaches an event bus for publishing task lifecycle events.
func (w *Worker) SetEventBus(bus *eventbus.Bus) {
	w.bus = bus
}

// Register adds a task handler to the worker.
func (w *Worker) Register(h TaskHandler) {
	w.handlers[h.Type()] = h
}

// Start runs the worker poll loop. Blocks until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) error {
	w.logger.Info("worker started",
		zap.String("id", w.id),
		zap.Strings("capabilities", w.capabilities),
		zap.Int("concurrency", w.concurrency),
		zap.Int("handlers", len(w.handlers)),
	)

	// Semaphore for concurrency control.
	sem := make(chan struct{}, w.concurrency)

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Drain semaphore to wait for in-flight tasks.
			for i := 0; i < w.concurrency; i++ {
				sem <- struct{}{}
			}
			return ctx.Err()
		case <-ticker.C:
			// Try to claim tasks up to available concurrency slots.
			for {
				select {
				case sem <- struct{}{}:
					// Got a slot, try to claim.
					task, err := w.store.ClaimTask(ctx, w.id, w.capabilities)
					if err != nil {
						w.logger.Error("failed to claim task", zap.Error(err))
						<-sem
						goto nextTick
					}
					if task == nil {
						<-sem // no task available
						goto nextTick
					}

					go func() {
						defer func() { <-sem }()
						w.executeTask(ctx, task)
					}()
				default:
					goto nextTick // all slots busy
				}
			}
		nextTick:
		}
	}
}

// extractChatID best-effort extracts chat_id from a JSON task payload.
func extractChatID(payload string) string {
	var p struct {
		ChatID string `json:"chat_id"`
	}
	_ = json.Unmarshal([]byte(payload), &p)
	return p.ChatID
}

func (w *Worker) executeTask(ctx context.Context, task *domain.Task) {
	handler, ok := w.handlers[task.Type]
	if !ok {
		w.logger.Error("no handler for task type",
			zap.String("type", task.Type),
			zap.Int64("task_id", task.ID))
		_ = w.store.FailTask(ctx, task.ID, fmt.Sprintf("no handler for type %q", task.Type))
		return
	}

	// Mark as running.
	if err := w.store.StartTask(ctx, task.ID, w.id); err != nil {
		w.logger.Error("failed to start task",
			zap.Int64("task_id", task.ID), zap.Error(err))
		return
	}

	w.logger.Info("executing task",
		zap.Int64("task_id", task.ID),
		zap.String("type", task.Type),
		zap.Int("attempt", task.Attempts+1))

	result, err := handler.Handle(ctx, task.Payload)
	if err != nil {
		w.logger.Error("task failed",
			zap.Int64("task_id", task.ID),
			zap.String("type", task.Type),
			zap.Error(err))
		_ = w.store.FailTask(ctx, task.ID, err.Error())
		if w.bus != nil {
			w.bus.Publish(ctx, eventbus.Event{
				Type: eventbus.TaskFailed,
				Payload: eventbus.TaskFailedPayload{
					TaskID:   task.ID,
					TaskType: task.Type,
					ChatID:   extractChatID(task.Payload),
					Error:    err.Error(),
					Attempt:  task.Attempts + 1,
				},
			})
		}
		return
	}

	if err := w.store.CompleteTask(ctx, task.ID, result); err != nil {
		w.logger.Error("failed to complete task",
			zap.Int64("task_id", task.ID), zap.Error(err))
		return
	}

	w.logger.Info("task completed",
		zap.Int64("task_id", task.ID),
		zap.String("type", task.Type))

	if w.bus != nil {
		w.bus.Publish(ctx, eventbus.Event{
			Type: eventbus.TaskCompleted,
			Payload: eventbus.TaskCompletedPayload{
				TaskID:   task.ID,
				TaskType: task.Type,
				ChatID:   extractChatID(task.Payload),
				Result:   result,
			},
		})
	}
}
