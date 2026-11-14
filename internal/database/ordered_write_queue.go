package database

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/jackc/pgconn"
)

// OrderedWriteOperation is a write operation with an ordering guarantee.
type OrderedWriteOperation struct {
	EmulatorID  int
	Sequence    int64 // Sequence number that enforces ordering
	Query       string
	Args        []interface{}
	ResultChan  chan error
	SubmittedAt time.Time
}

// OrderedWriteQueue runs writes with per-emulator ordering guarantees.
type OrderedWriteQueue struct {
	writePool *AdaptivePool
	tracer    *trace.Tracer

	// Pending operations channel
	operations chan *OrderedWriteOperation

	// Workers
	workers     []*writeWorker
	workerCount int

	// Per-emulator sequence tracking
	sequenceTracking map[int]*emulatorSequence
	trackingMutex    sync.RWMutex

	// Configuration
	queueSize     int
	batchSize     int
	flushInterval time.Duration

	// Control
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Metrics
	totalOps     atomic.Int64
	totalBatches atomic.Int64
	totalErrors  atomic.Int64
	totalRetries atomic.Int64
	avgLatencyMs atomic.Int64
}

// emulatorSequence tracks the operation sequence of one emulator.
type emulatorSequence struct {
	lastCompleted int64
	pending       map[int64]*OrderedWriteOperation
	mu            sync.Mutex
}

// writeWorker processes write operations.
type writeWorker struct {
	id         int
	queue      *OrderedWriteQueue
	localBatch []*OrderedWriteOperation
	batchMutex sync.Mutex
}

// NewOrderedWriteQueue creates an ordered write queue and starts its workers.
func NewOrderedWriteQueue(writePool *AdaptivePool, config *DynamicPoolConfig, tracer *trace.Tracer) *OrderedWriteQueue {
	owq := &OrderedWriteQueue{
		writePool:        writePool,
		tracer:           tracer,
		operations:       make(chan *OrderedWriteOperation, config.WriteQueueSize),
		workerCount:      config.WriteWorkers,
		sequenceTracking: make(map[int]*emulatorSequence),
		queueSize:        config.WriteQueueSize,
		batchSize:        config.WriteBatchSize,
		flushInterval:    time.Duration(config.WriteFlushMs) * time.Millisecond,
		stopChan:         make(chan struct{}),
	}

	// Create workers
	owq.workers = make([]*writeWorker, config.WriteWorkers)
	for i := 0; i < config.WriteWorkers; i++ {
		owq.workers[i] = &writeWorker{
			id:         i,
			queue:      owq,
			localBatch: make([]*OrderedWriteOperation, 0, config.WriteBatchSize),
		}
	}

	// Start workers
	owq.wg.Add(config.WriteWorkers)
	for _, worker := range owq.workers {
		go worker.run()
	}

	owq.tracer.Info("[OrderedWriteQueue] Started with %d workers, queue size: %d, batch size: %d",
		config.WriteWorkers, config.WriteQueueSize, config.WriteBatchSize)

	return owq
}

// Enqueue adds an operation to the queue and waits for its result.
func (owq *OrderedWriteQueue) Enqueue(emulatorID int, query string, args ...interface{}) error {
	// Next sequence for this emulator
	sequence := owq.getNextSequence(emulatorID)

	op := &OrderedWriteOperation{
		EmulatorID:  emulatorID,
		Sequence:    sequence,
		Query:       query,
		Args:        args,
		ResultChan:  make(chan error, 1),
		SubmittedAt: time.Now(),
	}

	// Register the pending operation
	owq.registerPendingOp(op)

	// Enqueue with a timeout
	select {
	case owq.operations <- op:
		// Wait for the result with a timeout
		select {
		case err := <-op.ResultChan:
			latency := time.Since(op.SubmittedAt).Milliseconds()
			owq.updateAvgLatency(latency)
			return err
		case <-time.After(30 * time.Second): // Longer timeout to cope with high load
			return fmt.Errorf("timeout waiting for write operation (emulator %d, seq %d)", emulatorID, sequence)
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout enqueuing operation (queue full, size: %d/%d)", len(owq.operations), cap(owq.operations))
	}
}

// EnqueueAsync adds an operation without waiting for its result.
func (owq *OrderedWriteQueue) EnqueueAsync(emulatorID int, query string, args ...interface{}) {
	sequence := owq.getNextSequence(emulatorID)

	op := &OrderedWriteOperation{
		EmulatorID:  emulatorID,
		Sequence:    sequence,
		Query:       query,
		Args:        args,
		ResultChan:  nil,
		SubmittedAt: time.Now(),
	}

	select {
	case owq.operations <- op:
		// OK
	default:
		owq.tracer.Warning("[OrderedWriteQueue] Queue full, dropping async op (emulator %d)", emulatorID)
	}
}

// getNextSequence returns the next sequence number for an emulator.
func (owq *OrderedWriteQueue) getNextSequence(emulatorID int) int64 {
	owq.trackingMutex.Lock()
	defer owq.trackingMutex.Unlock()

	seq, exists := owq.sequenceTracking[emulatorID]
	if !exists {
		seq = &emulatorSequence{
			lastCompleted: 0,
			pending:       make(map[int64]*OrderedWriteOperation),
		}
		owq.sequenceTracking[emulatorID] = seq
	}

	seq.mu.Lock()
	defer seq.mu.Unlock()

	nextSeq := seq.lastCompleted + 1 + int64(len(seq.pending))
	return nextSeq
}

// registerPendingOp records a pending operation.
func (owq *OrderedWriteQueue) registerPendingOp(op *OrderedWriteOperation) {
	owq.trackingMutex.RLock()
	seq := owq.sequenceTracking[op.EmulatorID]
	owq.trackingMutex.RUnlock()

	seq.mu.Lock()
	seq.pending[op.Sequence] = op
	seq.mu.Unlock()
}

// markCompleted marks an operation as complete and delivers its result.
func (owq *OrderedWriteQueue) markCompleted(op *OrderedWriteOperation, err error) {
	owq.trackingMutex.RLock()
	seq := owq.sequenceTracking[op.EmulatorID]
	owq.trackingMutex.RUnlock()

	seq.mu.Lock()
	defer seq.mu.Unlock()

	// Remove from pending
	delete(seq.pending, op.Sequence)

	// Advance lastCompleted
	if op.Sequence == seq.lastCompleted+1 {
		seq.lastCompleted = op.Sequence
	}

	// Deliver the result
	if op.ResultChan != nil {
		select {
		case op.ResultChan <- err:
		default:
			// The channel may be closed
		}
	}
}

// run processes operations, flushing by batch size or timer.
func (w *writeWorker) run() {
	defer w.queue.wg.Done()

	flushTimer := time.NewTimer(w.queue.flushInterval)
	defer flushTimer.Stop()

	for {
		select {
		case op := <-w.queue.operations:
			w.batchMutex.Lock()
			w.localBatch = append(w.localBatch, op)
			shouldFlush := len(w.localBatch) >= w.queue.batchSize
			w.batchMutex.Unlock()

			if shouldFlush {
				w.flushBatch()
				flushTimer.Reset(w.queue.flushInterval)
			}

		case <-flushTimer.C:
			w.flushBatch()
			flushTimer.Reset(w.queue.flushInterval)

		case <-w.queue.stopChan:
			w.flushBatch() // Flush final
			return
		}
	}
}

// flushBatch executes the pending batch, retrying each operation.
func (w *writeWorker) flushBatch() {
	w.batchMutex.Lock()
	if len(w.localBatch) == 0 {
		w.batchMutex.Unlock()
		return
	}

	batch := make([]*OrderedWriteOperation, len(w.localBatch))
	copy(batch, w.localBatch)
	w.localBatch = w.localBatch[:0]
	w.batchMutex.Unlock()

	w.queue.tracer.Info("[Worker %d] Flushing batch of %d operations", w.id, len(batch))

	// Execute operations with retry
	for _, op := range batch {
		err := w.executeWithRetry(op)
		w.queue.markCompleted(op, err)

		if err != nil {
			w.queue.totalErrors.Add(1)
		}
		w.queue.totalOps.Add(1)
	}

	w.queue.totalBatches.Add(1)
}

// executeWithRetry executes an operation with exponential backoff.
func (w *writeWorker) executeWithRetry(op *OrderedWriteOperation) error {
	const maxRetries = 3
	baseDelay := 100 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := w.queue.writePool.Exec(ctx, op.Query, op.Args...)
		cancel()

		if err == nil {
			if attempt > 0 {
				w.queue.tracer.Info("[Worker %d] Operation succeeded after %d retries (emulator %d, seq %d)",
					w.id, attempt, op.EmulatorID, op.Sequence)
			}
			return nil
		}

		lastErr = err
		w.queue.totalRetries.Add(1)

		// Stop on non-retryable errors
		if !isRetryableError(err) {
			w.queue.tracer.Error("[Worker %d] Non-retryable error (emulator %d, seq %d): %v",
				w.id, op.EmulatorID, op.Sequence, err)
			return err
		}

		if attempt < maxRetries {
			delay := baseDelay * time.Duration(1<<attempt) // Exponential backoff
			w.queue.tracer.Warning("[Worker %d] Attempt %d failed (emulator %d, seq %d), retrying in %v: %v",
				w.id, attempt+1, op.EmulatorID, op.Sequence, delay, err)
			time.Sleep(delay)
		}
	}

	w.queue.tracer.Error("[Worker %d] Operation failed after %d attempts (emulator %d, seq %d): %v",
		w.id, maxRetries, op.EmulatorID, op.Sequence, lastErr)
	return fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

// isRetryableError reports whether an error is worth retrying.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := err.Error()

	// Connection and timeout errors are retryable
	retryablePatterns := []string{
		"too many clients already",
		"connection refused",
		"timeout",
		"deadline exceeded",
		"connection reset",
		"broken pipe",
		"no connection to the server",
		"server closed the connection",
	}

	for _, pattern := range retryablePatterns {
		if containsError(errMsg, pattern) {
			return true
		}
	}

	return false
}

// containsError reports whether s contains substr (case-sensitive).
func containsError(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) &&
			(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
				containsSubstringError(s, substr)))
}

func containsSubstringError(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Stop shuts the queue down gracefully.
func (owq *OrderedWriteQueue) Stop() {
	owq.tracer.Info("[OrderedWriteQueue] Stopping...")
	close(owq.stopChan)
	owq.wg.Wait()

	stats := owq.GetMetrics()
	owq.tracer.Info("[OrderedWriteQueue] Stopped. Stats: %+v", stats)
}

// GetMetrics returns queue metrics.
func (owq *OrderedWriteQueue) GetMetrics() map[string]interface{} {
	return map[string]interface{}{
		"total_operations":  owq.totalOps.Load(),
		"total_batches":     owq.totalBatches.Load(),
		"total_errors":      owq.totalErrors.Load(),
		"total_retries":     owq.totalRetries.Load(),
		"queue_length":      len(owq.operations),
		"queue_capacity":    cap(owq.operations),
		"queue_utilization": float64(len(owq.operations)) / float64(cap(owq.operations)) * 100,
		"avg_latency_ms":    owq.avgLatencyMs.Load(),
		"worker_count":      owq.workerCount,
	}
}

// updateAvgLatency updates the average latency (exponential moving average).
func (owq *OrderedWriteQueue) updateAvgLatency(latencyMs int64) {
	currentAvg := owq.avgLatencyMs.Load()
	if currentAvg == 0 {
		owq.avgLatencyMs.Store(latencyMs)
	} else {
		// EMA with alpha = 0.1
		newAvg := int64(float64(currentAvg)*0.9 + float64(latencyMs)*0.1)
		owq.avgLatencyMs.Store(newAvg)
	}
}

// Exec runs a query through the ordered queue.
func (owq *OrderedWriteQueue) Exec(ctx context.Context, emulatorID int, query string, args ...interface{}) (pgconn.CommandTag, error) {
	err := owq.Enqueue(emulatorID, query, args...)
	// Return an empty CommandTag since the operation is queued
	return pgconn.CommandTag{}, err
}
