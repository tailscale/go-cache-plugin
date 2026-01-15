// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package gcsutil provides a client for Google Cloud Storage operations.
package gcsutil

import (
	"context"
	"fmt"
	"io"
	"io/fs"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Client is a wrapper for Google Cloud Storage operations.
type Client struct {
	client *storage.Client
	bucket string
}

// NewClient creates a new GCS client targeting the specified bucket.
func NewClient(ctx context.Context, bucket string, opts ...option.ClientOption) (*Client, error) {
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	return &Client{
		client: client,
		bucket: bucket,
	}, nil
}

// Get retrieves the object with the given key from GCS.
// The caller must close the returned reader when done.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj := c.client.Bucket(c.bucket).Object(key)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if err == storage.ErrObjectNotExist {
			return nil, 0, fs.ErrNotExist
		}
		return nil, 0, err
	}

	r, err := obj.NewReader(ctx)
	if err != nil {
		if err == storage.ErrObjectNotExist {
			return nil, 0, fs.ErrNotExist
		}
		return nil, 0, err
	}

	return r, attrs.Size, nil
}

// GetData returns the complete content of the object with the given key.
func (c *Client) GetData(ctx context.Context, key string) ([]byte, error) {
	r, _, err := c.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Put writes the data from the provided reader to the object with the given key.
func (c *Client) Put(ctx context.Context, key string, data io.Reader) error {
	w := c.client.Bucket(c.bucket).Object(key).NewWriter(ctx)
	_, err := io.Copy(w, data)
	if err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// PutCond performs a conditional put operation for the object with the given key.
// It only writes the data if the object doesn't exist or has a different content hash.
func (c *Client) PutCond(ctx context.Context, key, contentHash string, data io.Reader) (bool, error) {
	obj := c.client.Bucket(c.bucket).Object(key)
	attrs, err := obj.Attrs(ctx)
	if err == nil && attrs.Etag == contentHash {
		// Object exists with same hash, no need to upload
		return false, nil
	}

	w := obj.NewWriter(ctx)
	_, err = io.Copy(w, data)
	if err != nil {
		w.Close()
		return false, err
	}
	if err := w.Close(); err != nil {
		return false, err
	}
	return true, nil
}

// Close closes the GCS client and releases resources.
func (c *Client) Close() error {
	return c.client.Close()
}

// IsNotExist reports whether err indicates that a file or directory does not exist.
func IsNotExist(err error) bool {
	if err == fs.ErrNotExist {
		return true
	}
	if err, ok := err.(*googleapi.Error); ok {
		return err.Code == 404
	}
	return false
}
