// Package s3util defines some helpful utilities for working with S3.
package s3util

import (
	"cmp"
	"context"
	"errors"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
