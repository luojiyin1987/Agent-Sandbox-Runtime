package docker

import (
	"encoding/hex"
	"fmt"
	"strings"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const sha256DigestMarker = "@sha256:"

// WithMutableImageReference explicitly allows a trusted operator to configure
// a tag or other mutable Docker image reference. Production callers should not
// use this option: the default constructor requires a sha256 digest so the
// sandbox image cannot change between executions without a configuration
// change.
func WithMutableImageReference() Option {
	return func(b *Backend) error {
		b.allowMutableImage = true
		return nil
	}
}

func validateImageReference(image string, allowMutable bool) error {
	if allowMutable {
		return nil
	}

	marker := strings.LastIndex(image, sha256DigestMarker)
	if marker <= 0 || strings.Contains(image[:marker], "@") {
		return immutableImageReferenceError()
	}

	digest := image[marker+len(sha256DigestMarker):]
	if len(digest) != 64 || digest != strings.ToLower(digest) {
		return immutableImageReferenceError()
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return immutableImageReferenceError()
	}
	return nil
}

func immutableImageReferenceError() error {
	return fmt.Errorf(
		"%w: docker image must be pinned by sha256 digest (name@sha256:<64 lowercase hex>); mutable references require the trusted WithMutableImageReference option",
		sandbox.ErrInvalidRequest,
	)
}
