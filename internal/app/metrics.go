package app

import (
	"errors"
	"time"

	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

// ErrContention is the subset of ErrTransient caused by concurrency (lock
// timeout, deadlock, serialization failure) or a lost optimistic race.
var ErrContention = errors.New("app: concurrency contention")

// Metrics is the observability port of the use cases (Prometheus in production).
type Metrics interface {
	TransactionConcluded(channel string, t wagering.Snapshot, replay bool, elapsed time.Duration)
	ConcurrencyConflict(operation string)
	ReferenceAttempt(outcome string)
	OutboxPublished(n int)
	OutboxFailed(dead bool)
	ReconciliationChecked(consistent bool)
}

// NopMetrics discards everything (tests, tools).
type NopMetrics struct{}

func (NopMetrics) TransactionConcluded(string, wagering.Snapshot, bool, time.Duration) {}
func (NopMetrics) ConcurrencyConflict(string)                                          {}
func (NopMetrics) ReferenceAttempt(string)                                             {}
func (NopMetrics) OutboxPublished(int)                                                 {}
func (NopMetrics) OutboxFailed(bool)                                                   {}
func (NopMetrics) ReconciliationChecked(bool)                                          {}

// metrics returns the configured sink, defaulting to NopMetrics.
func (d Deps) metrics() Metrics {
	if d.Metrics == nil {
		return NopMetrics{}
	}
	return d.Metrics
}

func isContention(err error) bool {
	return errors.Is(err, ErrContention) || errors.Is(err, ErrConcurrentUpdate)
}
