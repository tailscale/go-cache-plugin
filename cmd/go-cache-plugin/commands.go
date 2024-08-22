package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/taskgroup"
	"github.com/tailscale/go-cache-plugin/s3cache"
)

var flags struct {
	CacheDir      string        `flag:"cache-dir,default=$GOCACHE_DIR,Local cache directory (required)"`
	S3Bucket      string        `flag:"bucket,default=$GOCACHE_S3_BUCKET,S3 bucket name (required)"`
	S3Region      string        `flag:"region,default=$GOCACHE_S3_REGION,S3 region"`
	KeyPrefix     string        `flag:"prefix,default=$GOCACHE_KEY_PREFIX,S3 key prefix (optional)"`
	MinUploadSize int64         `flag:"min-upload-size,default=$GOCACHE_MIN_SIZE,Minimum object size to upload to S3 (in bytes)"`
	Concurrency   int           `flag:"c,default=$GOCACHE_CONCURRENCY,Maximum number of concurrent requests"`
	S3Concurrency int           `flag:"u,default=$GOCACHE_S3_CONCURRENCY,Maximum concurrency for upload to S3"`
	PrintMetrics  bool          `flag:"m,default=$GOCACHE_METRICS,Print summary metrics to stderr at exit"`
	Expiration    time.Duration `flag:"x,default=$GOCACHE_EXPIRY,Cache expiration period (optional)"`
	Verbose       bool          `flag:"v,default=$GOCACHE_VERBOSE,Enable verbose logging"`
	DebugLog      bool          `flag:"debug,default=$GOCACHE_DEBUG,Enable detailed per-request debug logging (noisy)"`
}

func initCacheServer(env *command.Env) (*gocache.Server, error) {
	switch {
	case flags.CacheDir == "":
		return nil, env.Usagef("you must provide a --cache-dir")
	case flags.S3Bucket == "":
		return nil, env.Usagef("you must provide an S3 --bucket name")
	}
	region, err := getBucketRegion(env.Context(), flags.S3Bucket)
	if err != nil {
		return nil, env.Usagef("you must provide an S3 --region name")
	}

	dir, err := cachedir.New(flags.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("create local cache: %w", err)
	}

	cfg, err := config.LoadDefaultConfig(env.Context(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("laod AWS config: %w", err)
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
	return s, nil
}

// runDirect runs a cache communicating on stdin/stdout, for use as a direct
// GOCACHEPROG plugin.
func runDirect(env *command.Env) error {
	s, err := initCacheServer(env)
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

var remoteFlags struct {
	Socket string `flag:"socket,default=$GOCACHE_SOCKET,Socket path (required)"`
}

func noopClose(context.Context) error { return nil }

// runRemote runs a cache communicating over a Unix-domain socket.
func runRemote(env *command.Env) error {
	if remoteFlags.Socket == "" {
		return env.Usagef("you must provide a --socket path")
	}

	// Initialize the cache server. Unlike a direct server, only close down and
	// wait for cache cleanup when the whole process exits.
	s, err := initCacheServer(env)
	if err != nil {
		return err
	}
	closeHook := s.Close
	s.Close = noopClose

	// Listen for connections from the Go toolchain on the specified socket.
	lst, err := net.Listen("unix", remoteFlags.Socket)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer os.Remove(remoteFlags.Socket) // best-effort

	ctx, cancel := signal.NotifyContext(env.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		log.Printf("signal received, closing listener")
		lst.Close()
	}()

	var g taskgroup.Group
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
		if err := closeHook(context.Background()); err != nil {
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
