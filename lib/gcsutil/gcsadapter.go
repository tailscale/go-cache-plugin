package gcsutil

import (
	"context"
	"io"

	"github.com/tailscale/go-cache-plugin/lib/revproxy"
)

// GCSAdapter wraps a gcsutil.Client to implement the CacheClient interface.
type GCSAdapter struct {
	Client *Client
}

// NewGCSAdapter creates a new GCSAdapter that implements CacheClient.
func NewGCSAdapter(client *Client) *GCSAdapter {
	return &GCSAdapter{Client: client}
}

var _ revproxy.CacheClient = (*GCSAdapter)(nil)

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
