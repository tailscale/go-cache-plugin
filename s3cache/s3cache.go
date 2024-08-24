// Package s3cache implements callbacks for a gocache.Server that store data
// into an S3 bucket through a local directory.
package s3cache

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/taskgroup"
	"github.com/tailscale/go-cache-plugin/internal/s3util"
)

// Cache implements callbacks for a gocache.Server using an S3 bucket for
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
//	[<prefix>/]object/<xx>/<object-id>
//
// The object and action IDs are encoded as lower-case hexadecimal strings,
// with "<xx>" denoting the first two bytes of the ID to partition the space.
//
// The contents of each action file have the format:
//
//	<object-id> <timestamp>
//
// where the object ID is hex encoded and the timestamp is Unix nanoseconds.
// The object file contains just the binary data of the object.
type Cache struct {
	// Local is the local cache directory where actions and objects are staged.
	// It must be non-nil. A local stage is required because the Go toolchain
	// needs direct access to read the files reported by the cache.
	// It is safe to use a tmpfs directory.
	Local *cachedir.Dir

	// S3Client is the S3 client used to read and write cache entries to the
	// backing store. It must be non-nil.
	S3Client *s3.Client

	// S3Bucket is the name of the S3 bucket where cache entries are stored.
	// It must be non-empty.
	S3Bucket string

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
	start    func(taskgroup.Task) *taskgroup.Group

	getLocalHit  expvar.Int // count of Get hits in the local cache
	getFaultHit  expvar.Int // count of Get hits faulted in from S3
	getFaultMiss expvar.Int // count of Get faults that were misses
	putSkipSmall expvar.Int // count of "small" objects not written to S3
	putS3Found   expvar.Int // count of objects not written to S3 because they were already present
	putS3Action  expvar.Int // count of actions written to S3
	putS3Object  expvar.Int // count of objects written to S3
}

func (s *Cache) init() {
	s.initOnce.Do(func() {
		s.push, s.start = taskgroup.New(nil).Limit(s.uploadConcurrency())
	})
}

// Get implements the corresponding callback of the cache protocol.
func (s *Cache) Get(ctx context.Context, actionID string) (objectID, diskPath string, _ error) {
	objID, diskPath, err := s.Local.Get(ctx, actionID)
	if err == nil && objID != "" && diskPath != "" {
		s.getLocalHit.Add(1)
		return objID, diskPath, nil // cache hit, OK
	}

	// Reaching here, either we got a cache miss or an error reading from local.
	// Try reading the action from S3.
	act, err := s.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.S3Bucket,
		Key:    s.actionKey(actionID),
	})
	if err != nil {
		if s3util.IsNotExist(err) {
			s.getFaultMiss.Add(1)
			return "", "", nil // cache miss, OK
		}
		return "", "", fmt.Errorf("[s3] read action %s: %w", actionID, err)
	}

	// We got an action hit remotely, try to update the local copy.
	objectID, mtime, err := parseAction(act.Body)
	act.Body.Close()
	if err != nil {
		return "", "", err
	}

	obj, err := s.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.S3Bucket,
		Key:    s.objectKey(objectID),
	})
	if err != nil {
		// At this point we know the action exists, so if we can't read the
		// object report it as an error rather than a cache miss.
		return "", "", fmt.Errorf("[s3] read object %s: %w", objectID, err)
	}
	s.getFaultHit.Add(1)

	// Now we should have the body; poke it into the local cache.  Preserve the
	// modification timestamp recorded with the original action.
	defer obj.Body.Close()
	diskPath, err = s.Local.Put(ctx, gocache.Object{
		ActionID: actionID,
		ObjectID: objectID,
		Size:     *obj.ContentLength,
		Body:     obj.Body,
		ModTime:  mtime,
	})
	return objectID, diskPath, err
}

// Put implements the corresponding callback of the cache protocol.
func (s *Cache) Put(ctx context.Context, obj gocache.Object) (diskPath string, _ error) {
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
		mtime, err := s.maybePutObject(sctx, obj.ObjectID, diskPath, etr.ETag())
		if err != nil {
			return err
		}

		// Stage 2: Write the action record.
		if _, err := s.S3Client.PutObject(sctx, &s3.PutObjectInput{
			Bucket: &s.S3Bucket,
			Key:    s.actionKey(obj.ActionID),
			Body:   strings.NewReader(fmt.Sprintf("%s %d", obj.ObjectID, mtime.UnixNano())),
		}); err != nil {
			gocache.Logf(ctx, "write action %s: %v", obj.ActionID, err)
			return err
		}
		s.putS3Action.Add(1)
		return nil
	})

	return diskPath, nil
}

// Close implements the corresponding callback of the cache protocol.
func (s *Cache) Close(ctx context.Context) error {
	if s.push != nil {
		gocache.Logf(ctx, "waiting for uploads...")
		wstart := time.Now()
		err := s.push.Wait()
		gocache.Logf(ctx, "uploads complete (%v elapsed, err=%v)",
			time.Since(wstart).Round(10*time.Microsecond), err)
	}
	return nil
}

// SetMetrics implements the corresponding server callback.
func (s *Cache) SetMetrics(_ context.Context, m *expvar.Map) {
	m.Set("get_local_hit", &s.getLocalHit)
	m.Set("get_fault_hit", &s.getFaultHit)
	m.Set("get_fault_miss", &s.getFaultMiss)
	m.Set("put_skip_small", &s.putSkipSmall)
	m.Set("put_s3_found", &s.putS3Found)
	m.Set("put_s3_action", &s.putS3Action)
	m.Set("put_s3_object", &s.putS3Object)
}

// maybePutObject writes the specified object contents to S3 if there is not
// already a matching key with the same etag. It returns the modified time of
// the object file, whether or not it was sent to S3.
func (s *Cache) maybePutObject(ctx context.Context, objectID, diskPath, etag string) (time.Time, error) {
	f, err := os.Open(diskPath)
	if err != nil {
		gocache.Logf(ctx, "[s3] open local object %s: %v", objectID, err)
		return time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}

	key := s.objectKey(objectID)
	if _, err := s.S3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:  &s.S3Bucket,
		Key:     key,
		IfMatch: &etag,
	}); err == nil {
		s.putS3Found.Add(1)
		return fi.ModTime(), nil // already present and matching
	}

	if _, err := s.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.S3Bucket,
		Key:    s.objectKey(objectID),
		Body:   f,
	}); err != nil {
		gocache.Logf(ctx, "[s3] put object %s: %v", objectID, err)
		return fi.ModTime(), err
	}
	s.putS3Object.Add(1)
	return fi.ModTime(), nil
}

// makeKey assembles a complete key from the specified parts, including the key
// prefix if one is defined. The result is a pointer for compatibility with the
// S3 client library.
func (s *Cache) makeKey(parts ...string) *string {
	key := path.Join(s.KeyPrefix, path.Join(parts...))
	return &key
}

func (s *Cache) actionKey(id string) *string { return s.makeKey("action", id[:2], id) }
func (s *Cache) objectKey(id string) *string { return s.makeKey("object", id[:2], id) }

func (s *Cache) uploadConcurrency() int {
	if s.UploadConcurrency <= 0 {
		return runtime.NumCPU()
	}
	return s.UploadConcurrency
}

func parseAction(r io.Reader) (objectID string, mtime time.Time, _ error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", time.Time{}, err
	}
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
