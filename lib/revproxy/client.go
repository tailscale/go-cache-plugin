// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package revproxy implements a minimal HTTP reverse proxy that caches files
// locally on disk, backed by objects in a cloud storage bucket.
package revproxy

import (
	"context"
	"io"
	"io/fs"

	"github.com/tailscale/go-cache-plugin/lib/gcsutil"
	"github.com/tailscale/go-cache-plugin/lib/s3util"
)

// CacheClient defines the interface for storage backends used by the reverse proxy
// to persist cached responses in remote storage (such as S3 or GCS).
type CacheClient interface {
	// Get retrieves the object with the given key from the remote storage.
	// Returns a ReadCloser for the object data, the size of the object, and any error.
	// The caller must close the returned ReadCloser when done.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)

	// GetData is a convenience method that returns the complete content of the object
	// with the given key as a byte slice.
	GetData(ctx context.Context, key string) ([]byte, error)

	// Put writes the data from the provided reader to the object with the given key.
	// Returns an error if the operation fails.
	Put(ctx context.Context, key string, data io.Reader) error

	// PutCond performs a conditional put operation for the object with the given key.
	// It only writes the data if the object doesn't exist or has a different content hash.
	// Returns a boolean indicating whether the object was written, and any error.
	PutCond(ctx context.Context, key, contentHash string, data io.Reader) (bool, error)

	// Close releases any resources used by the client.
	Close() error
}

// S3Adapter wraps an s3util.Client to implement the CacheClient interface.
type S3Adapter struct {
	Client *s3util.Client
}

// NewS3Adapter creates a new S3Adapter that implements CacheClient.
func NewS3Adapter(client *s3util.Client) *S3Adapter {
	return &S3Adapter{Client: client}
}

// Get retrieves the object with the given key from S3.
func (a *S3Adapter) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return a.Client.Get(ctx, key)
}

// GetData returns the complete content of the object with the given key from S3.
func (a *S3Adapter) GetData(ctx context.Context, key string) ([]byte, error) {
	return a.Client.GetData(ctx, key)
}

// Put writes the data from the provided reader to S3 with the given key.
func (a *S3Adapter) Put(ctx context.Context, key string, data io.Reader) error {
	return a.Client.Put(ctx, key, data)
}

// PutCond performs a conditional put operation for the object with the given key in S3.
func (a *S3Adapter) PutCond(ctx context.Context, key, contentHash string, data io.Reader) (bool, error) {
	return a.Client.PutCond(ctx, key, contentHash, data)
}

// Close is a no-op for S3 since there's no need to close the client.
func (a *S3Adapter) Close() error {
	return nil
}

// GCSAdapter wraps a gcsutil.Client to implement the CacheClient interface.
type GCSAdapter struct {
	Client *gcsutil.Client
}

// NewGCSAdapter creates a new GCSAdapter that implements CacheClient.
func NewGCSAdapter(client *gcsutil.Client) *GCSAdapter {
	return &GCSAdapter{Client: client}
}

// Get retrieves the object with the given key from GCS.
func (a *GCSAdapter) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return a.Client.Get(ctx, key)
}

// GetData returns the complete content of the object with the given key from GCS.
func (a *GCSAdapter) GetData(ctx context.Context, key string) ([]byte, error) {
	return a.Client.GetData(ctx, key)
}

// Put writes the data from the provided reader to GCS with the given key.
func (a *GCSAdapter) Put(ctx context.Context, key string, data io.Reader) error {
	return a.Client.Put(ctx, key, data)
}

// PutCond performs a conditional put operation for the object with the given key in GCS.
func (a *GCSAdapter) PutCond(ctx context.Context, key, contentHash string, data io.Reader) (bool, error) {
	return a.Client.PutCond(ctx, key, contentHash, data)
}

// Close closes the GCS client and releases resources.
func (a *GCSAdapter) Close() error {
	return a.Client.Close()
}

// IsStorageNotExist returns true if the error indicates that the object doesn't exist in storage.
func IsStorageNotExist(err error) bool {
	return s3util.IsNotExist(err) || gcsutil.IsNotExist(err) || err == fs.ErrNotExist
}
