package docker

import (
	"errors"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const testImageDigest = "alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce"

func TestNewAcceptsImmutableImageDigest(t *testing.T) {
	backend, err := New(testImageDigest)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if backend.image != testImageDigest {
		t.Fatalf("backend image = %q, want %q", backend.image, testImageDigest)
	}
}

func TestNewRejectsMutableImageReferenceByDefault(t *testing.T) {
	for _, image := range []string{
		"alpine:3.22",
		"alpine:latest",
		"alpine@sha256:short",
		"alpine@sha256:14358309A308569C32BDC37E2E0E9694BE33A9D99E68AFB0F5FF33CC1F695DCE",
	} {
		t.Run(image, func(t *testing.T) {
			_, err := New(image)
			if !errors.Is(err, sandbox.ErrInvalidRequest) {
				t.Fatalf("New(%q) error = %v, want ErrInvalidRequest", image, err)
			}
		})
	}
}

func TestNewAllowsMutableImageReferenceOnlyWithTrustedOption(t *testing.T) {
	backend, err := New("alpine:3.22", WithMutableImageReference())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if backend.image != "alpine:3.22" || !backend.allowMutableImage {
		t.Fatalf("backend = %+v", backend)
	}
}
