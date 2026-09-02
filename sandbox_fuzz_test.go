package sandbox

import (
	"errors"
	"strings"
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

		err := request.Validate()
		if err != nil {
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
			}
			return
		}
		if request.Command == "" || strings.ContainsRune(argument, '\x00') || request.Timeout < 0 {
			t.Fatalf("Validate() accepted invalid basic fields: %+v", request)
		}
		if memory < 0 || request.Resources.MaxProcesses < 0 || output < 0 || milliCPUs < 0 {
			t.Fatalf("Validate() accepted negative resources: %+v", request.Resources)
		}
		switch request.Network.Mode {
		case "", NetworkNone, NetworkOutbound:
			if len(request.Network.Allow) != 0 {
				t.Fatalf("Validate() accepted allow entries without allowlist mode")
			}
		case NetworkAllowlist:
			if len(request.Network.Allow) == 0 {
				t.Fatalf("Validate() accepted an empty allowlist")
			}
		default:
			t.Fatalf("Validate() accepted network mode %q", request.Network.Mode)
		}
		switch request.Filesystem.Root {
		case "", RootReadOnly, RootReadWrite:
		default:
			t.Fatalf("Validate() accepted root mode %q", request.Filesystem.Root)
		}
	})
}
