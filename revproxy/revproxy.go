// Package revproxy implements a minimal HTTP reverse proxy that caches files
// locally on disk, backed by objects in an S3 bucket.
//
// # Limitations
//
// By default, only objects marked "immutable" by the target server are
// eligible to be cached. Volatile objects that specify a max-age are also
// cached in-memory, but are not persisted on disk or in S3. If we think it's
// worthwhile we can spend some time to add more elaborate cache pruning, but
// for now we're doing the simpler thing.
package revproxy

import (
	"bytes"
	"crypto/sha256"
	"expvar"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creachadair/taskgroup"
	"github.com/golang/groupcache/lru"
	"github.com/tailscale/go-cache-plugin/internal/s3util"
)

// Server is a caching reverse proxy server that caches successful responses to
// GET requests for certain designated domains.
//
// The host field of the request URL must match one of the configured targets.
// If not, the request is rejected with HTTP 502 (Bad Gateway).  Otherwise, the
// request is forwarded.  A successful response will be cached if the server's
// Cache-Control does not include "no-store", and does include "immutable".
//
// In addition, a successful response that is not immutable and specifies a
// max-age will be cached temporarily in-memory, up to the maximum of 1h.
//
// # Cache Format
//
// A cached response is a file with a header section and the body, separated by
// a blank line. Only a subset of response headers are saved.
//
// # Cache Responses
//
// For requests handled by the proxy, the response includes an "X-Cache" header
// indicating how the response was obtained:
//
//   - "hit, memory": The response was served out of the memory cache.
//   - "hit, local": The response was served out of the local cache.
//   - "hit, remote": The response was faulted in from S3.
//   - "fetch, cached": The response was forwarded to the target and cached.
//   - "fetch, uncached": The response was forwarded to the target and not cached.
//
// For results intersecting with the cache, it also reports a X-Cache-Id giving
// the storage key of the cache object.
type Server struct {
	// Targets is the list of hosts for which the proxy should forward requests.
	//
	// Each target is either a hostname ("host.domain.com"), which matches
	// hostnames exactly, or a pattern of the form "*.domain.com" which matches
	// hostnames like "domain.com" and "something.domain.com".
	Targets []string

	// Local is the path of a local cache directory where responses are cached.
	// It must be non-empty.
	Local string

	// S3Client is the S3 client used to read and write cache entries to the
	// backing store. It must be non-nil
	S3Client *s3util.Client

	// KeyPrefix, if non-empty, is prepended to each key stored into S3, with an
	// intervening slash.
	KeyPrefix string

	// Logf, if non-nil, is used to write log messages. If nil, logs are
	// discarded.
	Logf func(string, ...any)

	initOnce sync.Once
	tasks    *taskgroup.Group
	start    func(taskgroup.Task) *taskgroup.Group

	mcacheMu sync.Mutex // protects mcache
	mcache   *lru.Cache // short-lived mutable objects

	reqReceived  expvar.Int // total requests received
	reqMemoryHit expvar.Int // hit in memory cache (volatile)
	reqLocalHit  expvar.Int // hit in local cache
	reqLocalMiss expvar.Int // miss in local cache
	reqFaultHit  expvar.Int // hit in remote (S3) cache
	reqFaultMiss expvar.Int // miss in remote (S3) cache
	reqForward   expvar.Int // request forwarded directly to upstream
	rspSave      expvar.Int // successful response saved in local cache
	rspSaveMem   expvar.Int // response saved in memory cache
	rspSaveError expvar.Int // error saving to local cache
	rspSaveBytes expvar.Int // bytes written to local cache
	rspPush      expvar.Int // successful response saved in S3
	rspPushError expvar.Int // error saving to S3
	rspPushBytes expvar.Int // bytes written to S3
	rspNotCached expvar.Int // response not cached anywhere
}

func (s *Server) init() {
	s.initOnce.Do(func() {
		nt := runtime.NumCPU()
		s.tasks, s.start = taskgroup.New(nil).Limit(nt)
		s.mcache = lru.New(1 << 16)
	})
}

// Metrics returns a map of cache server metrics for s.  The caller is
// responsible to publish these metrics as desired.
func (s *Server) Metrics() *expvar.Map {
	m := new(expvar.Map)
	m.Set("req_received", &s.reqReceived)
	m.Set("req_memory_hit", &s.reqMemoryHit)
	m.Set("req_local_hit", &s.reqLocalHit)
	m.Set("req_local_miss", &s.reqLocalMiss)
	m.Set("req_fault_hit", &s.reqFaultHit)
	m.Set("req_fault_miss", &s.reqFaultMiss)
	m.Set("req_forward", &s.reqForward)
	m.Set("rsp_save", &s.rspSave)
	m.Set("rsp_save_memory", &s.rspSaveMem)
	m.Set("rsp_save_error", &s.rspSaveError)
	m.Set("rsp_save_bytes", &s.rspSaveBytes)
	m.Set("rsp_push", &s.rspPush)
	m.Set("rsp_push_error", &s.rspPushError)
	m.Set("rsp_push_bytes", &s.rspPushBytes)
	m.Set("rsp_not_cached", &s.rspNotCached)
	return m
}

// ServeHTTP implements the [http.Handler] interface for the proxy.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.init()
	s.reqReceived.Add(1)

	// Check whether this request is to a target we are permitted to proxy for.
	if !hostMatchesTarget(r.URL.Host, s.Targets) {
		s.logf("reject proxy request for non-target %q", r.URL)
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}

	hash := hashRequestURL(r.URL)
	canCache := s.canCacheRequest(r)
	if canCache {
		// Check for a hit on this object in the memory cache.
		if data, hdr, err := s.cacheLoadMemory(hash); err == nil {
			s.reqMemoryHit.Add(1)
			setXCacheInfo(hdr, "hit, memory", hash)
			writeCachedResponse(w, hdr, data)
			return
		}

		// Check for a hit on this object in the local cache.
		if data, hdr, err := s.cacheLoadLocal(hash); err == nil {
			s.reqLocalHit.Add(1)
			setXCacheInfo(hdr, "hit, local", hash)
			writeCachedResponse(w, hdr, data)
			return
		}
		s.reqLocalMiss.Add(1)

		// Fault in from S3.
		if data, hdr, err := s.cacheLoadS3(r.Context(), hash); err == nil {
			s.reqFaultHit.Add(1)
			if err := s.cacheStoreLocal(hash, hdr, data); err != nil {
				s.logf("update %q local: %v", hash, err)
			}
			setXCacheInfo(hdr, "hit, remote", hash)
			writeCachedResponse(w, hdr, data)
			return
		}
		s.reqFaultMiss.Add(1)
	}

	// Reaching here, the object is not already cached locally so we have to
	// talk to the backend to get it. We need to do this whether or not it is
	// cacheable. Note we handle each request with its own proxy instance, so
	// that we can handle each response in context of this request.
	s.reqForward.Add(1)
	proxy := &httputil.ReverseProxy{Rewrite: s.rewriteRequest}
	updateCache := func() {}
	if canCache {
		proxy.ModifyResponse = func(rsp *http.Response) error {
			maxAge, isVolatile := s.canMemoryCache(rsp)
			if !isVolatile && !s.canCacheResponse(rsp) {
				// A response we cannot cache at all.
				setXCacheInfo(rsp.Header, "fetch, uncached", "")
				s.rspNotCached.Add(1)
				return nil
			}

			// Read out the whole response body so we can update the cache, and
			// replace the response reader so we can copy it back to the caller.
			var buf bytes.Buffer
			rsp.Body = copyReader{
				Reader: io.TeeReader(rsp.Body, &buf),
				Closer: rsp.Body,
			}
			if isVolatile {
				setXCacheInfo(rsp.Header, "fetch, cached, volatile", hash)
				updateCache = func() {
					body := buf.Bytes()
					s.cacheStoreMemory(hash, maxAge, rsp.Header, body)
					s.rspSaveMem.Add(1)

					// N.B. Don't persist on disk or in S3.
				}
			} else {
				setXCacheInfo(rsp.Header, "fetch, cached", hash)
				updateCache = func() {
					body := buf.Bytes()
					if err := s.cacheStoreLocal(hash, rsp.Header, body); err != nil {
						s.rspSaveError.Add(1)
						s.logf("save %q to cache: %v", hash, err)

						// N.B.: Don't bother trying to forward to S3 in this case.
					} else {
						s.rspSave.Add(1)
						s.rspSaveBytes.Add(int64(len(body)))
						s.start(s.cacheStoreS3(hash, rsp.Header, body))
					}
				}
			}
			return nil
		}
	}
	proxy.ServeHTTP(w, r)
	updateCache()
}

// rewriteRequest rewrites the inbound request for routing to a target.
func (s *Server) rewriteRequest(pr *httputil.ProxyRequest) {
	pr.Out.URL = pr.In.URL
	pr.Out.URL.Scheme = "https"
	pr.Out.Host = pr.Out.URL.Host
}

type copyReader struct {
	io.Reader
	io.Closer
}

// makePath returns the local cache path for the specified request hash.
func (s *Server) makePath(hash string) string { return filepath.Join(s.Local, hash[:2], hash) }

// makeKey returns the S3 object key for the specified request hash.
func (s *Server) makeKey(hash string) string { return path.Join(s.KeyPrefix, hash[:2], hash) }

func (s *Server) logf(msg string, args ...any) {
	if s.Logf != nil {
		s.Logf(msg, args...)
	}
}

func hostMatchesTarget(host string, targets []string) bool {
	return slices.ContainsFunc(targets, func(s string) bool {
		if s == host {
			return true
		} else if tail, ok := strings.CutPrefix(s, "*"); ok {
			if strings.HasSuffix(host, tail) || host == strings.TrimPrefix(tail, ".") {
				return true
			}
		}
		return false
	})
}

// canCacheRequest reports whether r is a request whose response can be cached.
func (s *Server) canCacheRequest(r *http.Request) bool {
	return r.Method == "GET" && !slices.Contains(splitCacheControl(r.Header), "no-store")
}

// canCacheResponse reports whether r is a response whose body can be cached.
func (s *Server) canCacheResponse(rsp *http.Response) bool {
	if rsp.StatusCode != http.StatusOK {
		return false
	}
	cc := splitCacheControl(rsp.Header)
	return !slices.Contains(cc, "no-store") && slices.Contains(cc, "immutable")
}

// canMemoryCache reports whether r is a volatile response whose body can be
// cached temporarily, and if so returns the maxmimum length of time the cache
// entry should be valid for.
func (s *Server) canMemoryCache(rsp *http.Response) (time.Duration, bool) {
	if rsp.StatusCode != http.StatusOK {
		return 0, false
	}
	var maxAge time.Duration
	for _, v := range splitCacheControl(rsp.Header) {
		if v == "no-store" || v == "immutable" {
			return 0, false // don't cache immutable things in memory
		}
		sfx, ok := strings.CutPrefix(v, "max-age=")
		if !ok {
			continue
		}
		sec, err := strconv.Atoi(sfx)
		if err == nil {
			maxAge = time.Duration(min(sec, 3600)) * time.Second
		}
	}
	return maxAge, maxAge > 0
}

// hashRequest generates the storage digest for the specified request URL.
func hashRequestURL(u *url.URL) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(u.String())))
}

// writeCachedResponse generates an HTTP response for a cached result using the
// provided headers and body from the cache object.
func writeCachedResponse(w http.ResponseWriter, hdr http.Header, body []byte) {
	wh := w.Header()
	for name, vals := range hdr {
		for _, val := range vals {
			wh.Add(name, val)
		}
	}
	w.Write(body)
}

// splitCacheControl returns the tokens of the cache control header from h.
func splitCacheControl(h http.Header) []string {
	fs := strings.Split(h.Get("Cache-Control"), ",")
	for i, v := range fs {
		fs[i] = strings.TrimSpace(v)
	}
	return fs
}
