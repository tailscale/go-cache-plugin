// Package s3util defines some helpful utilities for working with S3.
package s3util

import (
	"cmp"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// IsNotExist reports whether err is an error indicating the requested resource
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

// BucketRegion reports the specified region for the given bucket using the
// GetBucketLocation API.
func BucketRegion(ctx context.Context, bucket string) (string, error) {
	// The default AWS region, which we use for resolving the bucket location
	// and also serves as the fallback if the API reports an empty region name.
	// The API returns "" for buckets in this region for historical reasons.
	const defaultRegion = "us-east-1"

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(defaultRegion))
	if err != nil {
		return "", err
	}
	cli := s3.NewFromConfig(cfg)
	loc, err := cli.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: &bucket})
	if err != nil {
		return "", err
	}
	return cmp.Or(string(loc.LocationConstraint), defaultRegion), nil
}

// NewETagReader returns a new S3 ETag reader for the contents of r.
func NewETagReader(r io.Reader) ETagReader {
	// Note: We use MD5 here because the S3 API requires it for an ETag, we do
	// not rely on it as a secure checksum.
	h := md5.New()
	return ETagReader{r: io.TeeReader(r, h), hash: h}
}

// ETagReader implements the [io.Reader] interface by delegating to a nested
// reader. The ETag method returns a correctly-formatted S3 ETag for all the
// data that have been read so far (initially none).
type ETagReader struct {
	r    io.Reader
	hash hash.Hash
}

// Read satisfies [io.Reader] by delegating to the wrapped reader.
func (e ETagReader) Read(data []byte) (int, error) { return e.r.Read(data) }

// ETag returns a correctly-formatted S3 etag for the contents of e that have
// been read so far.
func (e ETagReader) ETag() string { return fmt.Sprintf("%x", e.hash.Sum(nil)) }
