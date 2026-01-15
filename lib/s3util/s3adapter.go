package s3util

import (
	"context"
	"io"

	"github.com/tailscale/go-cache-plugin/lib/revproxy"
)

// S3Adapter wraps an s3util.Client to implement the CacheClient interface.
type S3Adapter struct {
	Client *Client
}

var _ revproxy.CacheClient = (*S3Adapter)(nil)

// NewS3Adapter creates a new S3Adapter that implements CacheClient.
func NewS3Adapter(client *Client) *S3Adapter {
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
