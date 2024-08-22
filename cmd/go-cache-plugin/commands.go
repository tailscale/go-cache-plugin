package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/tailscale/go-cache-plugin/s3cache"
)

// runLocal runs a cache locally communicating on stdin/stdout, for use as a
// direct GOCACHEPROG plugin.
func runLocal(env *command.Env) error {
	switch {
	case flags.CacheDir == "":
		return env.Usagef("You must provide a --cache-dir")
	case flags.S3Bucket == "":
		return env.Usagef("You must provide an S3 --bucket name")
	}
	region, err := getBucketRegion(env.Context(), flags.S3Bucket)
	if err != nil {
		return env.Usagef("You must provide an S3 --region name")
	}

	dir, err := cachedir.New(flags.CacheDir)
	if err != nil {
		return fmt.Errorf("create local cache: %w", err)
	}

	cfg, err := config.LoadDefaultConfig(env.Context(), config.WithRegion(region))
	if err != nil {
		return fmt.Errorf("laod AWS config: %w", err)
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
	if err := s.Run(env.Context(), os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("cache server exited with error: %w", err)
	}
	if flags.Verbose || flags.PrintMetrics {
		fmt.Fprintln(os.Stderr, s.Metrics())
	}
	return nil
}
