package docker

import (
	"fmt"
	"runtime"
	"sync"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const defaultMaxConcurrentSandboxes = 1

type admissionStatus uint8

const (
	admissionAccepted admissionStatus = iota
	admissionBusy
	admissionRequestTooLarge
)

type admissionDecision struct {
	status admissionStatus
	reason string
}

type admissionPool struct {
	mu          sync.Mutex
	limits      sandbox.AdmissionLimits
	active      int
	memoryBytes int64
	processes   int64
	outputBytes int64
	milliCPUs   int64
}

type reservationStats struct {
	active      int
	memoryBytes int64
	processes   int64
	outputBytes int64
	milliCPUs   int64
}

func defaultAdmissionLimits() sandbox.AdmissionLimits {
	return sandbox.AdmissionLimits{
		MaxConcurrent:  defaultMaxConcurrentSandboxes,
		MaxMemoryBytes: defaultMemoryBytes,
		MaxProcesses:   defaultProcesses,
		MaxOutputBytes: defaultMaxOutputBytes,
		MilliCPUs:      defaultMilliCPUs,
	}
}

func normalizeAdmissionLimits(limits sandbox.AdmissionLimits) (sandbox.AdmissionLimits, error) {
	if limits.MaxConcurrent < 0 || limits.MaxMemoryBytes < 0 || limits.MaxProcesses < 0 || limits.MaxOutputBytes < 0 || limits.MilliCPUs < 0 {
		return sandbox.AdmissionLimits{}, fmt.Errorf("%w: admission limits must not be negative", sandbox.ErrInvalidRequest)
	}

	defaults := defaultAdmissionLimits()
	if limits.MaxConcurrent == 0 {
		limits.MaxConcurrent = defaults.MaxConcurrent
	}
	if limits.MaxMemoryBytes == 0 {
		limits.MaxMemoryBytes = defaults.MaxMemoryBytes
	}
	if limits.MaxProcesses == 0 {
		limits.MaxProcesses = defaults.MaxProcesses
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if limits.MilliCPUs == 0 {
		limits.MilliCPUs = defaults.MilliCPUs
	}
	maxMilliCPUs := int64(runtime.NumCPU()) * 1000
	if limits.MilliCPUs > maxMilliCPUs {
		return sandbox.AdmissionLimits{}, fmt.Errorf("%w: admission milli-CPUs must not exceed node capacity %d", sandbox.ErrInvalidRequest, maxMilliCPUs)
	}
	return limits, nil
}

func newAdmissionPool(limits sandbox.AdmissionLimits) *admissionPool {
	if limits.MaxConcurrent == 0 {
		limits = defaultAdmissionLimits()
	}
	return &admissionPool{limits: limits}
}

func (p *admissionPool) tryAcquire(request sandbox.ResourceLimits) admissionDecision {
	p.mu.Lock()
	defer p.mu.Unlock()

	processes := int64(request.MaxProcesses)
	if request.MaxMemoryBytes > p.limits.MaxMemoryBytes {
		return admissionDecision{status: admissionRequestTooLarge, reason: "memory"}
	}
	if processes > int64(p.limits.MaxProcesses) {
		return admissionDecision{status: admissionRequestTooLarge, reason: "processes"}
	}
	if request.MaxOutputBytes > p.limits.MaxOutputBytes {
		return admissionDecision{status: admissionRequestTooLarge, reason: "output"}
	}
	if request.MilliCPUs > p.limits.MilliCPUs {
		return admissionDecision{status: admissionRequestTooLarge, reason: "cpu"}
	}
	if p.active >= p.limits.MaxConcurrent {
		return admissionDecision{status: admissionBusy, reason: "concurrency"}
	}
	if budgetExceeded(p.memoryBytes, request.MaxMemoryBytes, p.limits.MaxMemoryBytes) {
		return admissionDecision{status: admissionBusy, reason: "memory"}
	}
	if budgetExceeded(p.processes, processes, int64(p.limits.MaxProcesses)) {
		return admissionDecision{status: admissionBusy, reason: "processes"}
	}
	if budgetExceeded(p.outputBytes, request.MaxOutputBytes, p.limits.MaxOutputBytes) {
		return admissionDecision{status: admissionBusy, reason: "output"}
	}
	if budgetExceeded(p.milliCPUs, request.MilliCPUs, p.limits.MilliCPUs) {
		return admissionDecision{status: admissionBusy, reason: "cpu"}
	}

	p.active++
	p.memoryBytes += request.MaxMemoryBytes
	p.processes += processes
	p.outputBytes += request.MaxOutputBytes
	p.milliCPUs += request.MilliCPUs
	return admissionDecision{status: admissionAccepted}
}

func (p *admissionPool) release(request sandbox.ResourceLimits) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.active--
	p.memoryBytes -= request.MaxMemoryBytes
	p.processes -= int64(request.MaxProcesses)
	p.outputBytes -= request.MaxOutputBytes
	p.milliCPUs -= request.MilliCPUs
}

func (p *admissionPool) snapshot() reservationStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return reservationStats{
		active:      p.active,
		memoryBytes: p.memoryBytes,
		processes:   p.processes,
		outputBytes: p.outputBytes,
		milliCPUs:   p.milliCPUs,
	}
}

func budgetExceeded(used, requested, maximum int64) bool {
	return requested > maximum || used > maximum-requested
}

// WithAdmissionLimits sets trusted totals across active executions.
func WithAdmissionLimits(limits sandbox.AdmissionLimits) Option {
	return func(b *Backend) error {
		normalized, err := normalizeAdmissionLimits(limits)
		if err != nil {
			return err
		}
		b.admissionLimits = normalized
		return nil
	}
}
