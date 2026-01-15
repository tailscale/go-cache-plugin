// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package revproxy implements a minimal HTTP reverse proxy that caches files
// locally on disk, backed by objects in a cloud storage bucket.
package revproxy

import (
	"context"
	"io"
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
