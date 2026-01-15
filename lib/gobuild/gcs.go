// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package gobuild

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/taskgroup"
	"github.com/tailscale/go-cache-plugin/lib/gcsutil"
	"github.com/tailscale/go-cache-plugin/lib/revproxy"
	"github.com/tailscale/go-cache-plugin/lib/s3util"
)

// GCSCache implements callbacks for a gocache.Server using a GCS bucket for
// backing store with a local directory for staging.
//
// # Remote Cache Layout
//
// Within the designated GCS bucket, keys are organized into two groups. Each
// action is stored in a file named:
//
//	[<prefix>/]action/<xx>/<action-id>
//
// Each output object is stored in a file named:
//
//	[<prefix>/]output/<xx>/<object-id>
//
// The object and action IDs are encoded as lower-case hexadecimal strings,
// with "<xx>" denoting the first two bytes of the ID to partition the space.
//
// The contents of each action file have the format:
//
//	<output-id> <timestamp>
//
// where the object ID is hex encoded and the timestamp is Unix nanoseconds.
// The object file contains just the binary data of the object.
type GCSCache struct {
	// Local is the local cache directory where actions and objects are staged.
	// It must be non-nil. A local stage is required because the Go toolchain
	// needs direct access to read the files reported by the cache.
	// It is safe to use a tmpfs directory.
	Local *cachedir.Dir

	// GCSClient is the GCS client used to read and write cache entries to the
	// backing store. It must be non-nil.
	GCSClient *gcsutil.Client

	// KeyPrefix, if non-empty, is prepended to each key stored into GCS, with an
	// intervening slash.
	KeyPrefix string

	// MinUploadSize, if positive, defines a minimum object size in bytes below
	// which the cache will not write the object to GCS.
	MinUploadSize int64

	// UploadConcurrency, if positive, defines the maximum number of concurrent
	// tasks for writing cache entries to GCS.  If zero or negative, it uses
	// runtime.NumCPU.
	UploadConcurrency int

	// Tracks tasks pushing cache writes to GCS.
	initOnce sync.Once
	push     *taskgroup.Group
	start    func(taskgroup.Task)

	getLocalHit  expvar.Int // count of Get hits in the local cache
	getFaultHit  expvar.Int // count of Get hits faulted in from GCS
	getFaultMiss expvar.Int // count of Get faults that were misses
	putSkipSmall expvar.Int // count of "small" objects not written to GCS
	putGCSFound  expvar.Int // count of objects not written to GCS because they were already present
	putGCSAction expvar.Int // count of actions written to GCS
	putGCSObject expvar.Int // count of objects written to GCS
	putGCSError  expvar.Int // count of errors writing to GCS
}

var _ revproxy.Storage = (*GCSCache)(nil)

func (s *GCSCache) init() {
	s.initOnce.Do(func() {
		s.push, s.start = taskgroup.New(nil).Limit(s.uploadConcurrency())
	})
}

// Get implements the corresponding callback of the cache protocol.
func (s *GCSCache) Get(ctx context.Context, actionID string) (outputID, diskPath string, _ error) {
	s.init()

	objID, diskPath, err := s.Local.Get(ctx, actionID)
	if err == nil && objID != "" && diskPath != "" {
		s.getLocalHit.Add(1)
		return objID, diskPath, nil // cache hit, OK
	}

	// Reaching here, either we got a cache miss or an error reading from local.
	// Try reading the action from GCS.
	action, err := s.GCSClient.GetData(ctx, s.actionKey(actionID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.getFaultMiss.Add(1)
			return "", "", nil // cache miss, OK
		}
		return "", "", fmt.Errorf("[gcs] read action %s: %w", actionID, err)
	}

	// We got an action hit remotely, try to update the local copy.
	outputID, mtime, err := parseAction(action)
	if err != nil {
		return "", "", err
	}

	object, size, err := s.GCSClient.Get(ctx, s.outputKey(outputID))
	if err != nil {
		// At this point we know the action exists, so if we can't read the
		// object report it as an error rather than a cache miss.
		return "", "", fmt.Errorf("[gcs] read object %s: %w", outputID, err)
	}
	defer object.Close()
	s.getFaultHit.Add(1)

	// Now we should have the body; poke it into the local cache.  Preserve the
	// modification timestamp recorded with the original action.
	diskPath, err = s.Local.Put(ctx, gocache.Object{
		ActionID: actionID,
		OutputID: outputID,
		Size:     size,
		Body:     object,
		ModTime:  mtime,
	})
	return outputID, diskPath, err
}

// Put implements the corresponding callback of the cache protocol.
func (s *GCSCache) Put(ctx context.Context, obj gocache.Object) (diskPath string, _ error) {
	s.init()

	etr := s3util.NewETagReader(obj.Body)
	obj.Body = etr

	diskPath, err := s.Local.Put(ctx, obj)
	if err != nil {
		return "", err // don't bother trying to forward it to the remote
	}
	if obj.Size < s.MinUploadSize {
		s.putSkipSmall.Add(1)
		return diskPath, nil // don't bother uploading this, it's too small
	}

	// Try to push the record to GCS in the background.
	s.start(func() error {
		// Override the context with a separate timeout in case GCS is farkakte.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Minute)
		defer cancel()

		// Stage 1: Maybe write the object. Do this before writing the action
		// record so we are less likely to get a spurious miss later.
		f, err := os.Open(diskPath)
		if err != nil {
			s.putGCSError.Add(1)
			gocache.Logf(ctx, "[gcs] open local object %s: %v", obj.OutputID, err)
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			s.putGCSError.Add(1)
			gocache.Logf(ctx, "[gcs] stat local object %s: %v", obj.OutputID, err)
			return err
		}

		// Use PutCond to check if object already exists
		written, err := s.GCSClient.PutCond(sctx, s.outputKey(obj.OutputID), etr.ETag(), f)

		if err != nil {
			s.putGCSError.Add(1)
			gocache.Logf(ctx, "[gcs] put object %s: %v", obj.OutputID, err)
			return err
		}
		if written {
			s.putGCSObject.Add(1) // Actually uploaded
		} else {
			s.putGCSFound.Add(1) // Duplicate found, skipped upload
		}

		// Stage 2: Write the action record.
		if err := s.GCSClient.Put(sctx, s.actionKey(obj.ActionID),
			strings.NewReader(fmt.Sprintf("%s %d", obj.OutputID, fi.ModTime().UnixNano()))); err != nil {
			gocache.Logf(ctx, "[gcs] write action %s: %v", obj.ActionID, err)
			return err
		}
		s.putGCSAction.Add(1)
		return nil
	})

	return diskPath, nil
}

// Close implements the corresponding callback of the cache protocol.
func (s *GCSCache) Close(ctx context.Context) error {
	if s.push != nil {
		gocache.Logf(ctx, "waiting for uploads...")
		wstart := time.Now()
		s.push.Wait()
		gocache.Logf(ctx, "uploads complete (%v elapsed)", time.Since(wstart).Round(10*time.Microsecond))
	}
	return s.GCSClient.Close()
}

// SetMetrics implements the corresponding server callback.
func (s *GCSCache) SetMetrics(_ context.Context, m *expvar.Map) {
	m.Set("get_local_hit", &s.getLocalHit)
	m.Set("get_fault_hit", &s.getFaultHit)
	m.Set("get_fault_miss", &s.getFaultMiss)
	m.Set("put_skip_small", &s.putSkipSmall)
	m.Set("put_gcs_found", &s.putGCSFound)
	m.Set("put_gcs_action", &s.putGCSAction)
	m.Set("put_gcs_object", &s.putGCSObject)
	m.Set("put_gcs_error", &s.putGCSError)
}

// makeKey assembles a complete key from the specified parts, including the key
// prefix if one is defined.
func (s *GCSCache) makeKey(parts ...string) string {
	return path.Join(s.KeyPrefix, path.Join(parts...))
}

func (s *GCSCache) actionKey(id string) string { return s.makeKey("action", id[:2], id) }
func (s *GCSCache) outputKey(id string) string { return s.makeKey("output", id[:2], id) }

func (s *GCSCache) uploadConcurrency() int {
	if s.UploadConcurrency <= 0 {
		return runtime.NumCPU()
	}
	return s.UploadConcurrency
}
