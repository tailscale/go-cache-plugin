// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package gobuild implements callbacks for a gocache.Server that store data
// into an S3 bucket through a local directory.
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/taskgroup"
	"github.com/tailscale/go-cache-plugin/lib/s3util"
)

// S3Cache implements callbacks for a gocache.Server using an S3 bucket for
// backing store with a local directory for staging.
//
// # Remote Cache Layout
//
// Within the designated S3 bucket, keys are organized into two groups. Each
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
type S3Cache struct {
	// Local is the local cache directory where actions and objects are staged.
	// It must be non-nil. A local stage is required because the Go toolchain
	// needs direct access to read the files reported by the cache.
	// It is safe to use a tmpfs directory.
	Local *cachedir.Dir

	// S3Client is the S3 client used to read and write cache entries to the
	// backing store. It must be non-nil.
	S3Client *s3util.Client

	// KeyPrefix, if non-empty, is prepended to each key stored into S3, with an
	// intervening slash.
	KeyPrefix string

	// MinUploadSize, if positive, defines a minimum object size in bytes below
	// which the cache will not write the object to S3.
	MinUploadSize int64

	// UploadConcurrency, if positive, defines the maximum number of concurrent
	// tasks for writing cache entries to S3.  If zero or negative, it uses
	// runtime.NumCPU.
	UploadConcurrency int

	// Tracks tasks pushing cache writes to S3.
	initOnce sync.Once
	push     *taskgroup.Group
	start    func(taskgroup.Task)

	getLocalHit  expvar.Int // count of Get hits in the local cache
	getFaultHit  expvar.Int // count of Get hits faulted in from S3
	getFaultMiss expvar.Int // count of Get faults that were misses
	putSkipSmall expvar.Int // count of "small" objects not written to S3
	putS3Found   expvar.Int // count of objects not written to S3 because they were already present
	putS3Action  expvar.Int // count of actions written to S3
	putS3Object  expvar.Int // count of objects written to S3
	putS3Error   expvar.Int // count of errors writing to S3
}

func (s *S3Cache) init() {
	s.initOnce.Do(func() {
		s.push, s.start = taskgroup.New(nil).Limit(s.uploadConcurrency())
	})
}

// Get implements the corresponding callback of the cache protocol.
func (s *S3Cache) Get(ctx context.Context, actionID string) (outputID, diskPath string, _ error) {
	s.init()

	objID, diskPath, err := s.Local.Get(ctx, actionID)
	if err == nil && objID != "" && diskPath != "" {
		s.getLocalHit.Add(1)
		return objID, diskPath, nil // cache hit, OK
	}

	// Reaching here, either we got a cache miss or an error reading from local.
	// Try reading the action from S3.
	action, err := s.S3Client.GetData(ctx, s.actionKey(actionID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.getFaultMiss.Add(1)
			return "", "", nil // cache miss, OK
		}
		return "", "", fmt.Errorf("[s3] read action %s: %w", actionID, err)
	}

	// We got an action hit remotely, try to update the local copy.
	outputID, mtime, err := parseAction(action)
	if err != nil {
		return "", "", err
	}

	object, err := s.S3Client.Get(ctx, s.outputKey(outputID))
	if err != nil {
		// At this point we know the action exists, so if we can't read the
		// object report it as an error rather than a cache miss.
		return "", "", fmt.Errorf("[s3] read object %s: %w", outputID, err)
	}
	defer object.Close()
	s.getFaultHit.Add(1)

	// Now we should have the body; poke it into the local cache.  Preserve the
	// modification timestamp recorded with the original action.
	diskPath, err = s.Local.Put(ctx, gocache.Object{
		ActionID: actionID,
		OutputID: outputID,
		Body:     object,
		ModTime:  mtime,
	})
	return outputID, diskPath, err
}

// Put implements the corresponding callback of the cache protocol.
func (s *S3Cache) Put(ctx context.Context, obj gocache.Object) (diskPath string, _ error) {
	s.init()

	// Compute an etag so we can do a conditional put on the object data.
	// We do not rely on it as a secure checksum. The toolchain verifies the
	// content address against the bits we actually store.
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

	// Try to push the record to S3 in the background.
	s.start(func() error {
		// Override the context with a separate timeout in case S3 is farkakte.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Minute)
		defer cancel()

		// Stage 1: Maybe write the object. Do this before writing the action
		// record so we are less likely to get a spurious miss later.
		mtime, err := s.maybePutObject(sctx, obj.OutputID, diskPath, etr.ETag())
		if err != nil {
			return err
		}

		// Stage 2: Write the action record.
		if err := s.S3Client.Put(ctx, s.actionKey(obj.ActionID),
			strings.NewReader(fmt.Sprintf("%s %d", obj.OutputID, mtime.UnixNano()))); err != nil {
			gocache.Logf(ctx, "write action %s: %v", obj.ActionID, err)
			return err
		}
		s.putS3Action.Add(1)
		return nil
	})

	return diskPath, nil
}

// Close implements the corresponding callback of the cache protocol.
func (s *S3Cache) Close(ctx context.Context) error {
	if s.push != nil {
		gocache.Logf(ctx, "waiting for uploads...")
		wstart := time.Now()
		s.push.Wait()
		gocache.Logf(ctx, "uploads complete (%v elapsed)", time.Since(wstart).Round(10*time.Microsecond))
	}
	return nil
}

// SetMetrics implements the corresponding server callback.
func (s *S3Cache) SetMetrics(_ context.Context, m *expvar.Map) {
	m.Set("get_local_hit", &s.getLocalHit)
	m.Set("get_fault_hit", &s.getFaultHit)
	m.Set("get_fault_miss", &s.getFaultMiss)
	m.Set("put_skip_small", &s.putSkipSmall)
	m.Set("put_s3_found", &s.putS3Found)
	m.Set("put_s3_action", &s.putS3Action)
	m.Set("put_s3_object", &s.putS3Object)
	m.Set("put_s3_error", &s.putS3Error)
}

// maybePutObject writes the specified object contents to S3 if there is not
// already a matching key with the same etag. It returns the modified time of
// the object file, whether or not it was sent to S3.
func (s *S3Cache) maybePutObject(ctx context.Context, outputID, diskPath, etag string) (time.Time, error) {
	f, err := os.Open(diskPath)
	if err != nil {
		gocache.Logf(ctx, "[s3] open local object %s: %v", outputID, err)
		return time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}

	written, err := s.S3Client.PutCond(ctx, s.outputKey(outputID), etag, f)
	if err != nil {
		s.putS3Error.Add(1)
		gocache.Logf(ctx, "[s3] put object %s: %v", outputID, err)
		return fi.ModTime(), err
	}
	if written {
		s.putS3Found.Add(1)
		return fi.ModTime(), nil // already present and matching
	}
	s.putS3Object.Add(1)
	return fi.ModTime(), nil
}

// makeKey assembles a complete key from the specified parts, including the key
// prefix if one is defined.
func (s *S3Cache) makeKey(parts ...string) string {
	return path.Join(s.KeyPrefix, path.Join(parts...))
}

func (s *S3Cache) actionKey(id string) string { return s.makeKey("action", id[:2], id) }
func (s *S3Cache) outputKey(id string) string { return s.makeKey("output", id[:2], id) }

func (s *S3Cache) uploadConcurrency() int {
	if s.UploadConcurrency <= 0 {
		return runtime.NumCPU()
	}
	return s.UploadConcurrency
}

func parseAction(data []byte) (outputID string, mtime time.Time, _ error) {
	fs := strings.Fields(string(data))
	if len(fs) != 2 {
		return "", time.Time{}, errors.New("invalid action record")
	}
	ts, err := strconv.ParseInt(fs[1], 10, 64)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("invalid timestamp: %w", err)
	}
	return fs[0], time.Unix(ts/1e9, ts%1e9), nil
}
