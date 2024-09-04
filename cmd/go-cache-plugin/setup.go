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
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/mhttp/proxyconn"
	"github.com/creachadair/taskgroup"
	"github.com/creachadair/tlsutil"
	"github.com/goproxy/goproxy"
	"github.com/tailscale/go-cache-plugin/lib/s3util"
	"github.com/tailscale/go-cache-plugin/revproxy"
	"github.com/tailscale/go-cache-plugin/s3cache"
	"github.com/tailscale/go-cache-plugin/s3proxy"
	"tailscale.com/tsweb"
)

func initCacheServer(env *command.Env) (*gocache.Server, *s3util.Client, error) {
	switch {
	case flags.CacheDir == "":
		return nil, nil, env.Usagef("you must provide a --cache-dir")
	case flags.S3Bucket == "":
		return nil, nil, env.Usagef("you must provide an S3 --bucket name")
	}
	region, err := getBucketRegion(env.Context(), flags.S3Bucket)
	if err != nil {
		return nil, nil, env.Usagef("you must provide an S3 --region name")
	}

	dir, err := cachedir.New(flags.CacheDir)
	if err != nil {
		return nil, nil, fmt.Errorf("create local cache: %w", err)
	}

	cfg, err := config.LoadDefaultConfig(env.Context(), config.WithRegion(region))
	if err != nil {
		return nil, nil, fmt.Errorf("laod AWS config: %w", err)
	}

	vprintf("local cache directory: %s", flags.CacheDir)
	vprintf("S3 cache bucket %q (%s)", flags.S3Bucket, region)
	client := &s3util.Client{
		Client: s3.NewFromConfig(cfg),
		Bucket: flags.S3Bucket,
	}
	cache := &s3cache.Cache{
		Local:             dir,
		S3Client:          client,
		KeyPrefix:         flags.KeyPrefix,
		MinUploadSize:     flags.MinUploadSize,
		UploadConcurrency: flags.S3Concurrency,
	}
	cache.SetMetrics(env.Context(), expvar.NewMap("gocache_host"))

	close := cache.Close
	if flags.Expiration > 0 {
		dirClose := dir.Cleanup(flags.Expiration)
		close = func(ctx context.Context) error {
			return errors.Join(cache.Close(ctx), dirClose(ctx))
		}
	}
	s := &gocache.Server{
		Get:         cache.Get,
		Put:         cache.Put,
		Close:       close,
		SetMetrics:  cache.SetMetrics,
		MaxRequests: flags.Concurrency,
		Logf:        vprintf,
		LogRequests: flags.DebugLog,
	}
	expvar.Publish("gocache_server", s.Metrics().Get("server"))
	return s, client, nil
}

// initModProxy initializes a Go module proxy if one is enabled. If not, it
// returns a nil handler without error. The caller must defer a call to the
// cleanup function unless an error is reported.
func initModProxy(env *command.Env, s3c *s3util.Client) (_ http.Handler, cleanup func(), _ error) {
	if !serveFlags.ModProxy {
		return nil, noop, nil // OK, proxy is disabled
	} else if serveFlags.HTTP == "" {
		return nil, nil, env.Usagef("you must set --http to enable --modproxy")
	}

	modCachePath := filepath.Join(flags.CacheDir, "module")
	if err := os.MkdirAll(modCachePath, 0700); err != nil {
		return nil, nil, fmt.Errorf("create module cache: %w", err)
	}
	cacher := &s3proxy.Cacher{
		Local:       modCachePath,
		S3Client:    s3c,
		KeyPrefix:   path.Join(flags.KeyPrefix, "module"),
		MaxTasks:    flags.S3Concurrency,
		LogRequests: flags.DebugLog,
		Logf:        vprintf,
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
func initRevProxy(env *command.Env, s3c *s3util.Client, g *taskgroup.Group) (http.Handler, error) {
	if serveFlags.RevProxy == "" {
		return nil, nil // OK, proxy is disabled
	} else if serveFlags.HTTP == "" {
		return nil, env.Usagef("you must set --http to enable --revproxy")
	}

	revCachePath := filepath.Join(flags.CacheDir, "revproxy")
	if err := os.MkdirAll(revCachePath, 0700); err != nil {
		return nil, fmt.Errorf("create revproxy cache: %w", err)
	}
	hosts := strings.Split(serveFlags.RevProxy, ",")

	// Issue a server certificate so we can proxy HTTPS requests.
	cert, err := initServerCert(env, hosts)
	if err != nil {
		return nil, err
	}

	proxy := &revproxy.Server{
		Targets:   hosts,
		Local:     revCachePath,
		S3Client:  s3c,
		KeyPrefix: path.Join(flags.KeyPrefix, "revproxy"),
		Logf:      vprintf,
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

	g.Go(taskgroup.NoError(func() {
		<-env.Context().Done()
		vprintf("stopping proxy bridge")
		psrv.Shutdown(context.Background())
	}))

	expvar.Publish("revcache", proxy.Metrics())
	vprintf("enabling reverse proxy for %s", strings.Join(proxy.Targets, ", "))
	return bridge, nil
}

// initServerCert creates a signed certificate advertising the specified host
// names, for use in creating a TLS server.
func initServerCert(env *command.Env, hosts []string) (tls.Certificate, error) {
	ca, err := tlsutil.NewSigningCert(&x509.Certificate{
		Subject: pkix.Name{Organization: []string{"Tailscale build automation"}},
	}, 24*time.Hour)
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

	sc, err := tlsutil.NewServerCert(&x509.Certificate{
		Subject:  pkix.Name{Organization: []string{"Go cache plugin reverse proxy"}},
		DNSNames: hosts,
	}, 24*time.Hour, ca)
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
