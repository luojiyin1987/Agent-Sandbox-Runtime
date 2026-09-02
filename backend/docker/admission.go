package docker

import (
	"fmt"
	"sync"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const defaultMaxConcurrentSandboxes = 1

type admissionPool struct {
	mu            sync.Mutex
	maxConcurrent int
	aggregate     sandbox.ResourceLimits
	active        int
	memoryBytes   int64
	processes     int64
	outputBytes   int64
	milliCPUs     int64
}

func newAdmissionPool(maxConcurrent int, aggregate sandbox.ResourceLimits) *admissionPool {
	if maxConcurrent == 0 {
		maxConcurrent = defaultMaxConcurrentSandboxes
	}
	return &admissionPool{maxConcurrent: maxConcurrent, aggregate: aggregate}
}

func (p *admissionPool) tryAcquire(limits sandbox.ResourceLimits) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	processes := int64(limits.MaxProcesses)
	if p.active >= p.maxConcurrent ||
		budgetExceeded(p.memoryBytes, limits.MaxMemoryBytes, p.aggregate.MaxMemoryBytes) ||
		budgetExceeded(p.processes, processes, int64(p.aggregate.MaxProcesses)) ||
		budgetExceeded(p.outputBytes, limits.MaxOutputBytes, p.aggregate.MaxOutputBytes) ||
		budgetExceeded(p.milliCPUs, limits.MilliCPUs, p.aggregate.MilliCPUs) {
		return false
	}

	p.active++
	p.memoryBytes += limits.MaxMemoryBytes
	p.processes += processes
	p.outputBytes += limits.MaxOutputBytes
	p.milliCPUs += limits.MilliCPUs
	return true
}

func (p *admissionPool) release(limits sandbox.ResourceLimits) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.active--
	p.memoryBytes -= limits.MaxMemoryBytes
	p.processes -= int64(limits.MaxProcesses)
	p.outputBytes -= limits.MaxOutputBytes
	p.milliCPUs -= limits.MilliCPUs
}

func budgetExceeded(used, requested, maximum int64) bool {
	return maximum > 0 && (requested > maximum || used > maximum-requested)
}

// WithMaxConcurrentSandboxes sets the maximum executions for one backend instance.
func WithMaxConcurrentSandboxes(maximum int) Option {
	return func(b *Backend) error {
		if maximum <= 0 {
			return fmt.Errorf("%w: maximum concurrent sandboxes must be positive", sandbox.ErrInvalidRequest)
		}
		b.maxConcurrent = maximum
		return nil
	}
}

// WithAggregateResourceLimits sets optional totals across active executions.
func WithAggregateResourceLimits(limits sandbox.ResourceLimits) Option {
	return func(b *Backend) error {
		if limits.MaxMemoryBytes < 0 || limits.MaxProcesses < 0 || limits.MaxOutputBytes < 0 || limits.MilliCPUs < 0 {
			return fmt.Errorf("%w: aggregate resource limits must not be negative", sandbox.ErrInvalidRequest)
		}
		b.aggregateLimits = limits
		return nil
	}
}
