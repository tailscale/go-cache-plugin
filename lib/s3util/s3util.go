// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

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
	"io/fs"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/creachadair/mds/value"
)

// IsNotExist reports whether err is an error indicating the requested resource
// was not found, taking into account S3 and standard library types.
func IsNotExist(err error) bool {
	var e1 *types.NotFound
	var e2 *types.NoSuchKey
	if errors.As(err, &e1) || errors.As(err, &e2) {
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

// Client is a wrapper for an S3 client that provides basic read and write
// facilities to a specific bucket.
type Client struct {
	Client *s3.Client
	Bucket string
}

// Put writes the specified data to S3 under the given key.
func (c *Client) Put(ctx context.Context, key string, data io.Reader) error {
	// Attempt to find the size of the input to send as a content length.
	// If we can't do this, let the SDK figure it out.
	var sizePtr *int64
	switch t := data.(type) {
	case sizer:
		sizePtr = value.Ptr(t.Size())
	case statter:
		fi, err := t.Stat()
		if err == nil {
			sizePtr = value.Ptr(fi.Size())
		}
	case io.Seeker:
		v, err := t.Seek(0, io.SeekEnd)
		if err == nil {
			sizePtr = &v

			// Try to seek back to the beginning. If we cannot do this, fail out
			// so we don't try to write a partial object.
			_, err = t.Seek(0, io.SeekStart)
			if err != nil {
				return fmt.Errorf("[unexpected] seek failed: %w", err)
			}
		}
	}
	_, err := c.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &c.Bucket,
		Key:           &key,
		Body:          data,
		ContentLength: sizePtr,
	})
	return err
}

// Get returns the contents of the specified key from S3. On success, the
// returned reader contains the contents of the object, and the caller must
// close the reader when finished.
//
// If the key is not found, the resulting error satisfies [fs.ErrNotExist].
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	rsp, err := c.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.Bucket,
		Key:    &key,
	})
	if err != nil {
		if IsNotExist(err) {
			return nil, -1, fmt.Errorf("key %q: %w", key, fs.ErrNotExist)
		}
		return nil, -1, err
	}
	return rsp.Body, *rsp.ContentLength, nil
}

// GetData returns the contents of the specified key from S3. It is a shorthand
// for calling Get followed by io.ReadAll on the result.
func (c *Client) GetData(ctx context.Context, key string) ([]byte, error) {
	rc, _, err := c.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// PutCond writes the specified data to S3 under the given key if the key does
// not already exist, or if its content differs from the given etag.
// The etag is an MD5 of the expected contents, encoded as lowercase hex digits.
// On success, written reports whether the object was written.
func (c *Client) PutCond(ctx context.Context, key, etag string, data io.Reader) (written bool, _ error) {
	if _, err := c.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:  &c.Bucket,
		Key:     &key,
		IfMatch: &etag,
	}); err == nil {
		return false, nil
	}
	return true, c.Put(ctx, key, data)
}

// A sizer exports a Size method, e.g., [bytes.Reader] and similar.
type sizer interface{ Size() int64 }

// A statter exports a Stat method, e.g., [os.File] and similar.
type statter interface{ Stat() (fs.FileInfo, error) }
