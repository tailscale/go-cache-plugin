// Package s3util defines some helpful utilities for working with S3.
package s3util

import (
	"errors"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// IsNntExist reports whether err is an error indicating the requested resource
// was not found, taking into account S3 and standard library types.
func IsNotExist(err error) bool {
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	return errors.Is(err, os.ErrNotExist)
}
