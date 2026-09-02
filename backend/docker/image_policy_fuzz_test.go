package docker

import (
	"errors"
	"regexp"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

func FuzzValidateImageReference(f *testing.F) {
	immutablePattern := regexp.MustCompile(`(?s)^[^@]+@sha256:[0-9a-f]{64}$`)
	f.Add(testImageDigest, false)
	f.Add("alpine:latest", false)
	f.Add("alpine:latest", true)
	f.Fuzz(func(t *testing.T, image string, allowMutable bool) {
		err := validateImageReference(image, allowMutable)
		if allowMutable {
			if err != nil {
				t.Fatalf("validateImageReference() error = %v with mutable references allowed", err)
			}
			return
		}
		if err != nil {
			if !errors.Is(err, sandbox.ErrInvalidRequest) {
				t.Fatalf("validateImageReference() error = %v, want ErrInvalidRequest", err)
			}
			return
		}
		if !immutablePattern.MatchString(image) {
			t.Fatalf("validateImageReference() accepted non-canonical image %q", image)
		}
	})
}
