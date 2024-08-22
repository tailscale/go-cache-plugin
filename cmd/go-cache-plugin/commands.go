package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
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
