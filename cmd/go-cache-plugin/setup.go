// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"expvar"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/mhttp/proxyconn"
	"github.com/creachadair/taskgroup"
	"github.com/creachadair/tlsutil"
	"github.com/goproxy/goproxy"
	"github.com/tailscale/go-cache-plugin/lib/gcsutil"
	"github.com/tailscale/go-cache-plugin/lib/gobuild"
	"github.com/tailscale/go-cache-plugin/lib/modproxy"
	"github.com/tailscale/go-cache-plugin/lib/revproxy"
	"github.com/tailscale/go-cache-plugin/lib/s3util"
	"google.golang.org/api/option"
	"tailscale.com/tsweb"
)

func initCacheServer(env *command.Env) (*gocache.Server, revproxy.CacheClient, error) {
	// Validate required fields
	if flags.CacheDir == "" {
		return nil, nil, env.Usagef("you must provide a --cache-dir")
	}

	// Create the local cache directory
	dir, err := cachedir.New(flags.CacheDir)
	if err != nil {
		return nil, nil, fmt.Errorf("create local cache: %w", err)
	}

	vprintf("local cache directory: %s", flags.CacheDir)

	// Determine which storage backend to use
	storage := flags.StorageBackend
	if storage == "" {
		// For backward compatibility: if no specific backend is set but a bucket is provided
		// Check for multiple bucket flags
		hasMultipleBuckets := false
		bucketCount := 0
		if flags.Bucket != "" { bucketCount++ }
		if flags.GCSBucket != "" { bucketCount++ }
		if flags.S3Bucket != "" { bucketCount++ }
		if bucketCount > 1 {
			hasMultipleBuckets = true
		}
		
		if hasMultipleBuckets {
			return nil, nil, env.Usagef("you must provide only one bucket flag (--bucket, --gcs-bucket, or --s3-bucket)")
		}
		
		if flags.Bucket != "" {
			storage = "s3" // Default to S3 for backward compatibility
		} else if flags.GCSBucket != "" {
			storage = "gcs"
		} else if flags.S3Bucket != "" {
			storage = "s3"
		} else {
			return nil, nil, env.Usagef("you must provide a storage backend (--storage=s3|gcs) and appropriate bucket")
		}
	}

	// Storage client for the revproxy
	var storageClient revproxy.CacheClient

	// Set up the appropriate storage client and cache implementation
	var close func(context.Context) error
	var get func(context.Context, string) (string, string, error)
	var put func(context.Context, gocache.Object) (string, error)
	var setMetrics func(context.Context, *expvar.Map)

	// Initialize the storage client and cache implementation
	switch storage {
	case "gcs":
		// Validate GCS-specific parameters
		bucket := flags.GCSBucket
		if bucket == "" && flags.Bucket != "" {
			// For backward compatibility
			bucket = flags.Bucket
		}
		if bucket == "" {
			return nil, nil, env.Usagef("you must provide a --gcs-bucket name")
		}

		vprintf("GCS cache bucket: %s", bucket)

		// Initialize GCS client
		gcsClient, err := initGCSClient(env.Context(), bucket, flags.GCSKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize GCS client: %w", err)
		}

		// Create storage adapter for revproxy
		storageClient = revproxy.NewGCSAdapter(gcsClient)

		// Create GCS cache for gocache
		gcsCache := &gobuild.GCSCache{
			Local:             dir,
			GCSClient:         gcsClient,
			KeyPrefix:         flags.KeyPrefix,
			MinUploadSize:     flags.MinUploadSize,
			UploadConcurrency: flags.GCSConcurrency,
		}
		gcsCache.SetMetrics(env.Context(), expvar.NewMap("gocache_host"))

		// Set up the functions
		get = gcsCache.Get
		put = gcsCache.Put
		close = gcsCache.Close
		setMetrics = gcsCache.SetMetrics

	case "s3":
		// Validate S3-specific parameters
		bucket := flags.S3Bucket
		if bucket == "" && flags.Bucket != "" {
			// For backward compatibility
			bucket = flags.Bucket
		}
		if bucket == "" {
			return nil, nil, env.Usagef("you must provide a --s3-bucket name")
		}

		vprintf("S3 cache bucket: %s", bucket)

		// Initialize AWS S3 client
		s3Client, err := initS3Client(env.Context(), bucket, flags.S3Region, flags.S3Endpoint, flags.S3PathStyle)
		if err != nil {
			return nil, nil, fmt.Errorf("initialize S3 client: %w", err)
		}

		// Create storage adapter for revproxy
		storageClient = revproxy.NewS3Adapter(s3Client)

		// Create S3 cache for gocache
		s3Cache := &gobuild.S3Cache{
			Local:             dir,
			Client:           s3Client,
			KeyPrefix:         flags.KeyPrefix,
			MinUploadSize:     flags.MinUploadSize,
			UploadConcurrency: flags.S3Concurrency,
		}
		s3Cache.SetMetrics(env.Context(), expvar.NewMap("gocache_host"))

		// Set up the functions
		get = s3Cache.Get
		put = s3Cache.Put
		close = s3Cache.Close
		setMetrics = s3Cache.SetMetrics

	default:
		return nil, nil, env.Usagef("invalid storage backend: %q (must be 's3' or 'gcs')", storage)
	}

	// Add directory cleanup if requested
	if flags.Expiration > 0 {
		dirClose := dir.Cleanup(flags.Expiration)
		originalClose := close
		close = func(ctx context.Context) error {
			return errors.Join(originalClose(ctx), dirClose(ctx))
		}
	}

	// Create the server with the appropriate callback functions
	s := &gocache.Server{
		Get:         get,
		Put:         put,
		Close:       close,
		SetMetrics:  setMetrics,
		MaxRequests: flags.Concurrency,
		Logf:        vprintf,
		LogRequests: flags.DebugLog&debugBuildCache != 0,
	}
	expvar.Publish("gocache_server", s.Metrics().Get("server"))
	return s, storageClient, nil
}

// initGCSClient initializes a Google Cloud Storage client
func initGCSClient(ctx context.Context, bucket, keyFile string) (*gcsutil.Client, error) {
	// Set up options for GCS client creation
	var opts []option.ClientOption
	if keyFile != "" {
		// If a key file is specified, use it for authentication
		opts = append(opts, option.WithCredentialsFile(keyFile))
	}

	// Create the GCS client
	return gcsutil.NewClient(ctx, bucket, opts...)
}

// initS3Client initializes an Amazon S3 client
func initS3Client(ctx context.Context, bucket, region, endpoint string, pathStyle bool) (*s3util.Client, error) {
	// If region is not specified, try to resolve it from the bucket
	if region == "" {
		var err error
		region, err = s3util.BucketRegion(ctx, bucket)
		if err != nil {
			return nil, fmt.Errorf("resolve region for bucket %q: %w", bucket, err)
		}
	}
	vprintf("S3 region: %s", region)

	// Load the AWS configuration
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	// Create the S3 client with appropriate options
	opts := []func(*s3.Options){}
	if endpoint != "" {
		vprintf("S3 endpoint URL: %s", endpoint)
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}
	if pathStyle {
		vprintf("S3 path-style URLs enabled")
		opts = append(opts, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	}

	// Create the S3 client wrapper
	return &s3util.Client{
		Client: s3.NewFromConfig(cfg, opts...),
		Bucket: bucket,
	}, nil
}

type revproxyModproxyAdapter struct {
	client revproxy.CacheClient
}

func (a *revproxyModproxyAdapter) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return a.client.Get(ctx, key)
}

func (a *revproxyModproxyAdapter) GetData(ctx context.Context, key string) ([]byte, error) {
	return a.client.GetData(ctx, key)
}

func (a *revproxyModproxyAdapter) Put(ctx context.Context, key string, data io.Reader) error {
	return a.client.Put(ctx, key, data)
}

func mapToModproxyClient(c revproxy.CacheClient) modproxy.CacheClient {
	if c == nil {
		return nil
	}
	return &revproxyModproxyAdapter{client: c}
}

// initModProxy initializes a Go module proxy if one is enabled. If not, it
// returns a nil handler without error. The caller must defer a call to the
// cleanup function unless an error is reported.
func initModProxy(env *command.Env, client revproxy.CacheClient) (_ http.Handler, cleanup func(), _ error) {
	if !serveFlags.ModProxy {
		return nil, noop, nil // OK, proxy is disabled
	} else if serveFlags.HTTP == "" {
		return nil, nil, env.Usagef("you must set --http to enable --modproxy")
	}

	modCachePath := filepath.Join(flags.CacheDir, "module")
	if err := os.MkdirAll(modCachePath, 0755); err != nil {
		return nil, nil, fmt.Errorf("create module cache: %w", err)
	}
	// Create the module cacher with the appropriate storage backend
	cacher := &modproxy.S3Cacher{
		Local:     modCachePath,
		Client:    mapToModproxyClient(client),
		KeyPrefix: path.Join(flags.KeyPrefix, "module"),
		Logf:      vprintf,
	}
	cleanup = func() { vprintf("close cacher (err=%v)", cacher.Close()) }
	proxy := &goproxy.Goproxy{
		Fetcher: &goproxy.GoFetcher{
			// As configured, the fetcher should never shell out to the go
			// tool. Specifically, because we set GOPROXY and do not set any
			// bypass via GONOPROXY, GOPRIVATE, etc., we will only attempt to
			// proxy for the specific server(s) listed in Env.
			GoBin: "/bin/false",
			Env:   []string{"GOPROXY=https://proxy.golang.org"},
		},
		Cacher:        cacher,
		ProxiedSumDBs: []string{"sum.golang.org"}, // default, see below
	}
	vprintf("enabling Go module proxy")
	if serveFlags.SumDB != "" {
		proxy.ProxiedSumDBs = strings.Split(serveFlags.SumDB, ",")
		vprintf("enabling sum DB proxy for %s", strings.Join(proxy.ProxiedSumDBs, ", "))
	}
	expvar.Publish("modcache", cacher.Metrics())
	return http.StripPrefix("/mod", proxy), cleanup, nil
}

// initRevProxy initializes a reverse proxy if one is enabled.  If not, it
// returns nil, nil to indicate a proxy was not requested. Otherwise, it
// returns a [http.Handler] to dispatch reverse proxy requests.
//
// The reverse proxy runs two collaborating HTTP servers:
//
//   - The "inner" server is the proxy itself, which checks for cached values,
//     forwards client requests to the remote origin (if necessary), and
//     updates the cache with responses. The [revproxy.Server] is a lightweight
//     wrapper around [net/http/httputil.ReverseProxy].
//
//   - The "outer" server is a bridge, that intercepts client requests.  The
//     bridge forwards plain HTTP requests directly to the inner server.  For
//     HTTPS CONNECT requests, the bridge hijacks the client connection and
//     terminates TLS using a locally-signed certificate, and forwards the
//     decrypted client requests to the inner caching proxy.
//
// The outer bridge is what receives requests routed by the main HTTP endpoint;
// the inner server gets all its input via the bridge:
//
//	                          +------------+    +--------+
//	client --[proxy-request]->|HTTP handler+--->| bridge +--CONNECT--+
//	                          +------------+    +---+----+           |
//	                                                |                |
//	                                               HTTP              v
//	                          +-------------+       |        +---------------+
//	            [response]<---| cache proxy |<------+--------+ terminate TLS |
//	                          +-------------+                +---------------+
//
// To the main HTTP listener, the bridge is an [http.Handler] that serves
// requests routed to it. To the inner server, the bridge is a [net.Listener],
// a source of client connections (with TLS terminated).
func initRevProxy(env *command.Env, storageClient revproxy.CacheClient, g *taskgroup.Group) (http.Handler, error) {
	if serveFlags.RevProxy == "" {
		return nil, nil // OK, proxy is disabled
	} else if serveFlags.HTTP == "" {
		return nil, env.Usagef("you must set --http to enable --revproxy")
	}

	revCachePath := filepath.Join(flags.CacheDir, "revproxy")
	if err := os.MkdirAll(revCachePath, 0755); err != nil {
		return nil, fmt.Errorf("create revproxy cache: %w", err)
	}
	hosts := strings.Split(serveFlags.RevProxy, ",")

	// Issue a server certificate so we can proxy HTTPS requests.
	cert, err := initServerCert(env, hosts)
	if err != nil {
		return nil, err
	}

	proxy := &revproxy.Server{
		Targets:     hosts,
		Local:       revCachePath,
		Storage:     storageClient,
		KeyPrefix:   path.Join(flags.KeyPrefix, "revproxy"),
		Logf:        vprintf,
		LogRequests: flags.DebugLog&debugRevProxy != 0,
	}
	bridge := &proxyconn.Bridge{
		Addrs:   hosts,
		Handler: proxy, // forward HTTP requests unencrypted to the proxy
		Logf:    vprintf,

		// Forward connections not matching Addrs directly to their targets.
		ForwardConnect: true,
	}
	expvar.Publish("proxyconn", bridge.Metrics())

	// Run the proxy on its own separate server with TLS support.  This server
	// does not listen on a real network; it receives connections forwarded by
	// the bridge internally from successful CONNECT requests.
	psrv := &http.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},

		// Ordinarly HTTP proxy requests are delegated directly.
		Handler: proxy,
	}
	g.Go(func() error { return psrv.ServeTLS(bridge, "", "") })

	g.Run(func() {
		<-env.Context().Done()
		vprintf("stopping proxy bridge")
		psrv.Shutdown(context.Background())
	})

	expvar.Publish("revcache", proxy.Metrics())
	vprintf("enabling reverse proxy for %s", strings.Join(proxy.Targets, ", "))
	return bridge, nil
}

// initServerCert creates a signed certificate advertising the specified host
// names, for use in creating a TLS server.
func initServerCert(env *command.Env, hosts []string) (tls.Certificate, error) {
	ca, err := tlsutil.NewSigningCert(24*time.Hour, &x509.Certificate{
		Subject: pkix.Name{Organization: []string{"Tailscale build automation"}},
	})
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate signing cert: %w", err)
	}
	if err := installSigningCert(env, ca); err != nil {
		vprintf("WARNING: %v", err)
	} else {
		vprintf("installed signing cert in system store")

		// TODO(creachadair): We should probably clean up old expired certs.
		// This is OK for ephemeral build/CI workers, though.
	}

	sc, err := tlsutil.NewServerCert(24*time.Hour, ca, &x509.Certificate{
		Subject:  pkix.Name{Organization: []string{"Go cache plugin reverse proxy"}},
		DNSNames: hosts,
	})
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate server cert: %w", err)
	}

	return sc.TLSCertificate()
}

// makeHandler returns an HTTP handler that dispatches requests to debug
// handlers or to the specified proxies, if they are defined.
func makeHandler(modProxy, revProxy http.Handler) http.HandlerFunc {
	mux := http.NewServeMux()
	tsweb.Debugger(mux)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "" && r.URL.Host == r.Host {
			// The caller wants us to proxy for them.
			if revProxy != nil {
				revProxy.ServeHTTP(w, r)
				return
			}
			// We don't allow proxying in this configuration, bug off.
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
			return
		}

		path := r.URL.Path
		if strings.HasPrefix(path, "/debug/") {
			mux.ServeHTTP(w, r)
			return
		}
		if modProxy != nil && r.Method == http.MethodGet && strings.HasPrefix(path, "/mod/") {
			modProxy.ServeHTTP(w, r)
			return
		}
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	}
}

// noop is a cleanup function that does nothing, used as a default.
func noop() {}
