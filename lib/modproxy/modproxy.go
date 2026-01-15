// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package modproxy implements components of a Go module proxy that caches
// files locally on disk, backed by objects in a cloud storage bucket (S3 or GCS).
package modproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"expvar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/creachadair/atomicfile"
	"github.com/creachadair/taskgroup"
	"github.com/goproxy/goproxy"
	"github.com/tailscale/go-cache-plugin/lib/revproxy"
	"golang.org/x/sync/semaphore"
)

var _ goproxy.Cacher = (*StorageCacher)(nil)

// StorageCacher implements the [github.com/goproxy/goproxy.Cacher] interface using
// a local disk cache backed by a cloud storage bucket (S3 or GCS).
//
// # Cache Layout
//
// Module cache files are stored under a SHA256 digest of the filename
// presented to the cache, encoded as hex and partitioned by the first two
// bytes of the digest:
//
// For example:
//
//	SHA256("fizzlepug") → 160db4d719252162c87a9169e26deda33d2340770d0d540fd4c580c55008b2d6
//	<cache-dir>/module/16/160db4d719252162c87a9169e26deda33d2340770d0d540fd4c580c55008b2d6
//
// When files are stored in cloud storage, the same naming convention is used, but with
// the specified key prefix instead:
//
//	<key-prefix>/module/16/0db4d719252162c87a9169e26deda33d2340770d0d540fd4c580c55008b2d6
type StorageCacher struct {
	// Local is the path of a local cache directory where modules are cached.
	// It must be non-empty.
	Local string

	// Client is the storage client used to read and write cache entries to the
	// backing store (S3 or GCS). It must be non-nil.
	Client revproxy.CacheClient

	// KeyPrefix, if non-empty, is prepended to each key stored in storage, with an
	// intervening slash.
	KeyPrefix string

	// MaxTasks, if positive, limits the number of concurrent tasks that may be
	// interacting with cloud storage. If zero or negative, the default is
	// [runtime.NumCPU].
	MaxTasks int

	// Logf, if non-nil, is used to write log messages. If nil, logs are
	// discarded.
	Logf func(string, ...any)

	// LogRequests, if true, enables detailed (but noisy) debug logging of all
	// requests handled by the cache. Logs are written to Logf.
	//
	// Each result is presented in the format:
	//
	//    B <op> "<n>" (<digest>)
	//    E <op> "<n>", err=<e>, <time> elapsed
	//
	// Where the operations are "GET" and "PUT". The "B" line is when the
	// operation began, and "E" when it ended. When a GET operation successfully
	// faults in a result from cloud storage, the log is:
	//
	//    F GET "<n>" hit (<digest>)
	//
	// When a PUT operation finishes writing a value behind to cloud storage, the log is:
	//
	//    W PUT "<n>", err=<e>, <time> elapsed
	//
	LogRequests bool

	// Tracks tasks interacting with cloud storage in the background.
	initOnce sync.Once
	tasks    *taskgroup.Group
	start    func(taskgroup.Task)
	sema     *semaphore.Weighted

	pathError       expvar.Int // errors constructing file paths
	getRequest      expvar.Int // total number of Get requests
	getLocalHit     expvar.Int // get: hit in local directory
	getLocalMiss    expvar.Int // get: miss in local directory
	getFaultHit     expvar.Int // get: hit in remote storage
	getFaultMiss    expvar.Int // get: miss in remote storage
	getLocalError   expvar.Int // get: error reading the local directory
	getFaultError   expvar.Int // get: error reading from storage
	getLocalBytes   expvar.Int // get: total bytes fetched from the local directory
	getStorageBytes expvar.Int // get: total bytes fetched from storage
	putRequest      expvar.Int // total number of Put requests
	putLocalHit     expvar.Int // put: put of object already stored locally
	putLocalError   expvar.Int // put: error writing the local directory
	putStorageError expvar.Int // put: error writing to storage
	putLocalBytes   expvar.Int // put: total bytes written to the local directory
	putStorageBytes expvar.Int // put: total bytes written to storage
}

func (c *StorageCacher) init() {
	c.initOnce.Do(func() {
		nt := c.MaxTasks
		if nt <= 0 {
			nt = runtime.NumCPU()
		}
		c.tasks, c.start = taskgroup.New(nil).Limit(nt)
		c.sema = semaphore.NewWeighted(int64(nt))
	})
}

// Get implements a method of the goproxy.Cacher interface.  It reports cache
// hits out of the local directory if available, or faults in from S3.
func (c *StorageCacher) Get(ctx context.Context, name string) (_ io.ReadCloser, oerr error) {
	c.init()
	c.getRequest.Add(1)
	start := time.Now()
	hash, path, err := c.makePath(name)

	c.vlogf("mc B GET %q (%s)", name, hash)
	defer func() { c.vlogf("mc E GET %q, err=%v, %v elapsed", name, oerr, time.Since(start)) }()

	if err != nil {
		return nil, err
	}

	// Check whether the file already exists locally.
	if rc, size, err := openReader(path); err == nil {
		c.getLocalHit.Add(1)
		c.getLocalBytes.Add(size)
		return rc, nil
	} else if errors.Is(err, os.ErrNotExist) {
		c.getLocalMiss.Add(1)
	} else {
		c.getLocalError.Add(1)
		c.logf("get %q local: %v (treating as miss)", name, err)
	}

	// Local cache miss, fault in from cloud storage.
	if err := c.sema.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	defer c.sema.Release(1)

	obj, _, err := c.Client.Get(ctx, c.makeKey(hash))
	if errors.Is(err, fs.ErrNotExist) {
		c.getFaultMiss.Add(1)
		return nil, err
	} else if err != nil {
		c.getFaultError.Add(1)
		return nil, err
	}
	defer obj.Close()
	c.getFaultHit.Add(1)
	c.vlogf("mc F GET %q hit (%s)", name, hash)

	if _, err := c.putLocal(ctx, name, path, obj); err != nil {
		return nil, err
	}
	rc, _, err := openReader(path)
	return rc, err
}

// putLocal reports whether the specified path already exists in the local
// cache, and if not, writes data atomically into the path.
func (c *StorageCacher) putLocal(ctx context.Context, name, path string, data io.Reader) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	}
	nw, err := atomicfile.WriteAll(path, data, 0644)
	c.putLocalBytes.Add(nw)
	if err != nil {
		c.putLocalError.Add(1)
	}
	return false, err
}

// Put implements a method of the goproxy.Cacher interface. It stores data into
// the local directory and then writes it back to S3 in the background.
func (c *StorageCacher) Put(ctx context.Context, name string, data io.ReadSeeker) (oerr error) {
	c.init()
	c.putRequest.Add(1)
	start := time.Now()
	hash, path, err := c.makePath(name)

	c.vlogf("mc B PUT %q (%s)", name, hash)
	defer func() { c.vlogf("mc E PUT %q, err=%v, %v elapsed", name, oerr, time.Since(start)) }()

	if err != nil {
		return err
	}

	if ok, err := c.putLocal(ctx, name, path, data); err != nil {
		return err
	} else if ok {
		c.putLocalHit.Add(1)
		return nil
	}

	// Try to push the object to cloud storage in the background.
	f, size, err := openFileSize(path)
	if err != nil {
		c.putLocalError.Add(1)
		return err
	}
	c.start(func() error {
		defer f.Close()
		start := time.Now()

		// Override the context with a separate timeout in case the storage service is farkakte.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Minute)
		defer cancel()

		if err := c.Client.Put(sctx, c.makeKey(hash), f); err != nil {
			c.putStorageError.Add(1)
			c.logf("[storage] put %q failed: %v", name, err)
		} else {
			c.putStorageBytes.Add(size)
		}
		c.vlogf("mc W PUT %q, err=%v %v elapsed", name, err, time.Since(start))
		return err
	})
	return nil
}

// Close waits until all background updates are complete.
func (c *StorageCacher) Close() error {
	c.init()
	return c.tasks.Wait()
}

// Metrics returns a map of cacher metrics. The caller is responsible for
// publishing these metrics.
func (c *StorageCacher) Metrics() *expvar.Map {
	m := new(expvar.Map)
	m.Set("path_error", &c.pathError)
	m.Set("get_request", &c.getRequest)
	m.Set("get_local_hit", &c.getLocalHit)
	m.Set("get_local_miss", &c.getLocalMiss)
	m.Set("get_fault_hit", &c.getFaultHit)
	m.Set("get_fault_miss", &c.getFaultMiss)
	m.Set("get_local_error", &c.getLocalError)
	m.Set("get_local_bytes", &c.getLocalBytes)
	m.Set("get_storage_bytes", &c.getStorageBytes)
	m.Set("put_request", &c.putRequest)
	m.Set("put_local_hit", &c.putLocalHit)
	m.Set("put_local_error", &c.putLocalError)
	m.Set("put_storage_error", &c.putStorageError)
	m.Set("put_local_bytes", &c.putLocalBytes)
	m.Set("put_storage_bytes", &c.putStorageBytes)
	return m
}

func hashName(name string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
}

// makeKey assembles a complete storage key from the specified parts, including the
// key prefix if one is defined.
func (c *StorageCacher) makeKey(hash string) string {
	return path.Join(c.KeyPrefix, hash[:2], hash)
}

// makePath assembles a complete local cache path for the given name, creating
// the enclosing directory if needed.
func (c *StorageCacher) makePath(name string) (hash, path string, err error) {
	hash = hashName(name)
	path = filepath.Join(c.Local, hash[:2], hash)
	err = os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		c.pathError.Add(1)
	}
	return hash, path, err
}

func (c *StorageCacher) logf(msg string, args ...any) {
	if c.Logf != nil {
		c.Logf(msg, args...)
	}
}

func (c *StorageCacher) vlogf(msg string, args ...any) {
	if c.LogRequests {
		c.logf(msg, args...)
	}
}

func openReader(path string) (_ io.ReadCloser, size int64, _ error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func openFileSize(path string) (io.ReadCloser, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}
