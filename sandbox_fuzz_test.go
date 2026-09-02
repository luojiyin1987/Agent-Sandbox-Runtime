package sandbox

import (
	"errors"
	"testing"
	"time"
)

func FuzzValidate(f *testing.F) {
	f.Add("true", "argument", int64(0), int64(0), int64(0), int64(0), int64(0), "", "", false)
	f.Add("sh", "bad\x00argument", int64(-1), int64(-1), int64(-1), int64(-1), int64(-1), "host", "host", true)
	f.Fuzz(func(
		t *testing.T,
		command string,
		argument string,
		timeout int64,
		memory int64,
		processes int64,
		output int64,
		milliCPUs int64,
		networkMode string,
		rootMode string,
		withAllowEntry bool,
	) {
		request := ExecRequest{
			Command: command,
			Args:    []string{argument},
			Timeout: time.Duration(timeout),
			Resources: ResourceLimits{
				MaxMemoryBytes: memory,
				MaxProcesses:   int(processes),
				MaxOutputBytes: output,
				MilliCPUs:      milliCPUs,
			},
			Network:    NetworkPolicy{Mode: NetworkMode(networkMode)},
			Filesystem: FilesystemPolicy{Root: RootFilesystemMode(rootMode)},
		}
		if withAllowEntry {
			request.Network.Allow = []string{"example.com:443"}
		}

		if err := request.Validate(); err != nil && !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
		}
	})
}
