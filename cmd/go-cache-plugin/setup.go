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
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/gocache"
	"github.com/creachadair/gocache/cachedir"
	"github.com/creachadair/tlsutil"
	"github.com/tailscale/go-cache-plugin/internal/s3util"
	"github.com/tailscale/go-cache-plugin/s3cache"
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

func initServerCert(env *command.Env, hosts []string) (tls.Certificate, error) {
	ca, err := tlsutil.NewSigningCert(&x509.Certificate{
		Subject: pkix.Name{Organization: []string{"Tailscale build automation"}},
	}, 24*time.Hour)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate signing cert: %w", err)
	}
	// TODO(creachadair): Install the CA someplace

	sc, err := tlsutil.NewServerCert(&x509.Certificate{
		Subject:  pkix.Name{Organization: []string{"Go cache plugin reverse proxy"}},
		DNSNames: hosts,
	}, 24*time.Hour, ca)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate server cert: %w", err)
	}

	return sc.TLSCertificate()
}
