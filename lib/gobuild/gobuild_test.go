// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package gobuild_test

import (
	"context"
	"expvar"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/tailscale/go-cache-plugin/lib/gobuild"
	"github.com/tailscale/go-cache-plugin/lib/s3util"
)

// fakeS3 is a minimal in-memory S3 endpoint supporting HEAD and PUT on
// path-style object URLs.
type fakeS3 struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodHead:
		if _, ok := f.data[r.URL.Path]; !ok {
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.data[r.URL.Path] = body
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestPutMetrics(t *testing.T) {
	srv := httptest.NewServer(&fakeS3{data: make(map[string][]byte)})
	defer srv.Close()

	local, err := cachedir.New(t.TempDir())
	if err != nil {
		t.Fatalf("Create local cache: %v", err)
	}
	cache := &gobuild.S3Cache{
		Local: local,
		S3Client: &s3util.Client{
			Client: s3.New(s3.Options{
				BaseEndpoint: aws.String(srv.URL),
				Region:       "us-east-1",
				Credentials:  credentials.NewStaticCredentialsProvider("id", "secret", ""),
				UsePathStyle: true,
			}),
			Bucket: "test-bucket",
		},
	}

	ctx := context.Background()
	var metrics expvar.Map
	cache.SetMetrics(ctx, &metrics)
	count := func(name string) string { return metrics.Get(name).String() }

	const (
		outputID = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
		action1  = "11bbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
		action2  = "22bbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	)
	put := func(actionID string) {
		t.Helper()
		if _, err := cache.Put(ctx, gocache.Object{
			ActionID: actionID,
			OutputID: outputID,
			Size:     5,
			Body:     strings.NewReader("hello"),
		}); err != nil {
			t.Fatalf("Put %s: %v", actionID, err)
		}
		if err := cache.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	// The first write stores the object in S3.
	put(action1)
	if got := count("put_s3_object"); got != "1" {
		t.Errorf("After new object: put_s3_object = %s, want 1", got)
	}
	if got := count("put_s3_found"); got != "0" {
		t.Errorf("After new object: put_s3_found = %s, want 0", got)
	}

	// A second action with the same output finds the object already present.
	put(action2)
	if got := count("put_s3_object"); got != "1" {
		t.Errorf("After existing object: put_s3_object = %s, want 1", got)
	}
	if got := count("put_s3_found"); got != "1" {
		t.Errorf("After existing object: put_s3_found = %s, want 1", got)
	}
	if got := count("put_s3_action"); got != "2" {
		t.Errorf("put_s3_action = %s, want 2", got)
	}
}
