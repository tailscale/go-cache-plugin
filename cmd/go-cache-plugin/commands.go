package main

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/taskgroup"
	"github.com/goproxy/goproxy"
	"github.com/tailscale/go-cache-plugin/s3cache"
	"github.com/tailscale/go-cache-plugin/s3proxy"
	"tailscale.com/tsweb"
)

var flags struct {
	CacheDir      string        `flag:"cache-dir,default=$GOCACHE_DIR,Local cache directory (required)"`
	S3Bucket      string        `flag:"bucket,default=$GOCACHE_S3_BUCKET,S3 bucket name (required)"`
	S3Region      string        `flag:"region,default=$GOCACHE_S3_REGION,S3 region"`
	KeyPrefix     string        `flag:"prefix,default=$GOCACHE_KEY_PREFIX,S3 key prefix (optional)"`
	MinUploadSize int64         `flag:"min-upload-size,default=$GOCACHE_MIN_SIZE,Minimum object size to upload to S3 (in bytes)"`
	Concurrency   int           `flag:"c,default=$GOCACHE_CONCURRENCY,Maximum number of concurrent requests"`
	S3Concurrency int           `flag:"u,default=$GOCACHE_S3_CONCURRENCY,Maximum concurrency for upload to S3"`
	PrintMetrics  bool          `flag:"metrics,default=$GOCACHE_METRICS,Print summary metrics to stderr at exit"`
	Expiration    time.Duration `flag:"expiry,default=$GOCACHE_EXPIRY,Cache expiration period (optional)"`
	Verbose       bool          `flag:"v,default=$GOCACHE_VERBOSE,Enable verbose logging"`
	DebugLog      bool          `flag:"debug,default=$GOCACHE_DEBUG,Enable detailed per-request debug logging (noisy)"`
}

func initCacheServer(env *command.Env) (*gocache.Server, *s3.Client, error) {
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
	cache := &s3cache.Cache{
		Local:             dir,
		S3Client:          s3.NewFromConfig(cfg),
		S3Bucket:          flags.S3Bucket,
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
	return s, cache.S3Client, nil
}

// runDirect runs a cache communicating on stdin/stdout, for use as a direct
// GOCACHEPROG plugin.
func runDirect(env *command.Env) error {
	s, _, err := initCacheServer(env)
	if err != nil {
		return err
	}
	if err := s.Run(env.Context(), os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("cache server exited with error: %w", err)
	}
	if flags.Verbose || flags.PrintMetrics {
		fmt.Fprintln(os.Stderr, s.Metrics())
	}
	return nil
}

var serveFlags struct {
	Socket   string `flag:"socket,default=$GOCACHE_SOCKET,Socket path (required)"`
	ModProxy string `flag:"modproxy,default=$GOCACHE_MODPROXY,Module proxy service address ([host]:port)"`
	SumDB    string `flag:"sumdb,default=$GOCACHE_SUMDB,SumDB servers to proxy for (comma-separated)"`
}

func noopClose(context.Context) error { return nil }

// runServe runs a cache communicating over a Unix-domain socket.
func runServe(env *command.Env) error {
	if serveFlags.Socket == "" {
		return env.Usagef("you must provide a --socket path")
	}

	// Initialize the cache server. Unlike a direct server, only close down and
	// wait for cache cleanup when the whole process exits.
	s, s3c, err := initCacheServer(env)
	if err != nil {
		return err
	}
	closeHook := s.Close
	s.Close = noopClose

	// Listen for connections from the Go toolchain on the specified socket.
	lst, err := net.Listen("unix", serveFlags.Socket)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer os.Remove(serveFlags.Socket) // best-effort

	ctx, cancel := signal.NotifyContext(env.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		log.Printf("signal received, closing listener")
		lst.Close()
	}()

	// If a module proxy is enabled, start it.
	var g taskgroup.Group
	if serveFlags.ModProxy != "" {
		modCachePath := filepath.Join(flags.CacheDir, "module")
		if err := os.MkdirAll(modCachePath, 0700); err != nil {
			lst.Close()
			return fmt.Errorf("create module cache: %w", err)
		}
		cacher := &s3proxy.Cacher{
			Local:       modCachePath,
			S3Client:    s3c,
			S3Bucket:    flags.S3Bucket,
			KeyPrefix:   path.Join(flags.KeyPrefix, "module"),
			MaxTasks:    flags.S3Concurrency,
			LogRequests: flags.DebugLog,
			Logf:        vprintf,
		}
		defer func() {
			vprintf("close cacher (err=%v)", cacher.Close())
		}()
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
		if serveFlags.SumDB != "" {
			proxy.ProxiedSumDBs = strings.Split(serveFlags.SumDB, ",")
			vprintf("enabling sum DB proxy for %s", strings.Join(proxy.ProxiedSumDBs, ", "))
		}
		expvar.Publish("modcache", cacher.Metrics())

		// Run an HTTP server exporting the proxy and debug metrics.
		mux := http.NewServeMux()
		mux.Handle("/", proxy)
		tsweb.Debugger(mux)
		srv := &http.Server{
			Addr:    serveFlags.ModProxy,
			Handler: mux,
		}
		g.Go(srv.ListenAndServe)
		vprintf("started module proxy at %q", serveFlags.ModProxy)
		go func() {
			<-ctx.Done()
			vprintf("signal received, stopping module proxy")
			srv.Shutdown(context.Background())
		}()
	}

	for {
		conn, err := lst.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("accept failed: %v, exiting server loop", err)
			}
			break
		}
		log.Printf("new client connection")
		g.Go(func() error {
			defer func() {
				log.Printf("client connection closed")
				conn.Close()
			}()
			return s.Run(ctx, conn, conn)
		})
	}
	log.Printf("server loop exited, waiting for client exit")
	g.Wait()
	if closeHook != nil {
		ctx := gocache.WithLogf(context.Background(), log.Printf)
		if err := closeHook(ctx); err != nil {
			log.Printf("server close: %v (ignored)", err)
		}
	}
	return nil
}

// runConnect implements a direct cache proxy by connecting to a remote server
// over a Unix-domain socket.
func runConnect(env *command.Env, socketPath string) error {
	if socketPath == "" {
		return env.Usagef("you must provide a socket path")
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("dial socket: %w", err)
	}
	start := time.Now()
	vprintf("connected to %q", socketPath)

	out := taskgroup.Go(func() error {
		_, err := io.Copy(os.Stdout, conn)
		return err
	})

	_, rerr := io.Copy(conn, os.Stdin)
	if rerr != nil {
		vprintf("error sending: %v", rerr)
	}
	conn.Close()
	out.Wait()
	vprintf("connection closed (%v elapsed)", time.Since(start))
	return nil
}
