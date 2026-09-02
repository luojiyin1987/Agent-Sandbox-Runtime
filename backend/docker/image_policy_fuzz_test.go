package docker

import (
	"errors"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

func FuzzValidateImageReference(f *testing.F) {
	f.Add(testImageDigest, false)
	f.Add("alpine:latest", false)
	f.Add("alpine:latest", true)
	f.Fuzz(func(t *testing.T, image string, allowMutable bool) {
		if err := validateImageReference(image, allowMutable); err != nil && !errors.Is(err, sandbox.ErrInvalidRequest) {
			t.Fatalf("validateImageReference() error = %v, want ErrInvalidRequest", err)
		}
	})
}
