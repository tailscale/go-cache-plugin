// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Program gocache implements the experimental GOCACHEPROG protocol over an S3
// bucket, for use in builder and CI workers.
package main

import (
	"context"
	"log"
	"os"

	"github.com/creachadair/command"
	"github.com/creachadair/flax"
	"github.com/tailscale/go-cache-plugin/lib/s3util"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	root := &command.C{
		Name:  command.ProgramName(),
		Usage: "--cache-dir d --bucket b [options]\nhelp",
		Help: `Run a cache service for the Go toolchain backed by an S3 bucket.

This program serves the Go toolchain cache protocol on stdin/stdout.
It is meant to be run by the "go" tool as a GOCACHEPROG plugin.
For example:

    GOCACHEPROG=` + command.ProgramName() + ` go build ./...

Note that this requires a Go toolchain built with GOEXPERIMENT=cacheprog.

You must provide --cache-dir, --bucket, and --region, or the corresponding
environment variables (see "help environment").  Entries in the cache are
stored in the specified S3 bucket, and staged in a local directory specified by
the --cache-dir flag or GOCACHE_DIR environment.`,

		SetFlags: command.Flags(flax.MustBind, &flags),
		Run:      command.Adapt(runDirect),

		Commands: []*command.C{
			{
				Name:  "serve",
				Usage: "--plugin <port>",
				Help: `Run a cache server.

In this mode, the cache server listens for connections on a socket instead of
serving directly over stdin/stdout. The "connect" command adapts the direct
interface to this one.

By default, only the build cache is exported via the --plugin port.

If --http is set, the server also exports an HTTP server at that address.
By default, this exports only /debug endpoints, including metrics.
When --http is enabled, the following options are available:

- When --modcache is true, the server also exports a caching module proxy at
  http://<host>:<port>/mod/.

- When --revproxy is set, the server also hosts a caching reverse proxy for the
  specified hosts at http://<host>:<port>. The reverse proxy handles both HTTP
  and HTTPS requests, and caches immutable successful responses.`,

				SetFlags: command.Flags(flax.MustBind, &serveFlags),
				Run:      command.Adapt(runServe),
			},
			{
				Name:  "connect",
				Usage: "<port>",
				Help: `Connect to a remote cache server.

This mode bridges stdin/stdout to a cache server (see the "serve" command)
listening on the specified port.`,

				Run: command.Adapt(runConnect),
			},
			command.HelpCommand(helpTopics),
			command.VersionCommand(),
		},
	}
	command.RunOrFail(root.NewEnv(nil), os.Args[1:])
}

// getBucketRegion reports the specified region for the given bucket.
// if the --region flag was set, that value is returned without error.
// Otherwise, it queries the GetBucketLocation API.
func getBucketRegion(ctx context.Context, bucket string) (string, error) {
	if flags.S3Region != "" {
		return flags.S3Region, nil
	}
	return s3util.BucketRegion(ctx, bucket)
}

// vprintf acts as log.Printf if the --verbose flag is set; otherwise it
// discards its input.
func vprintf(msg string, args ...any) {
	if flags.Verbose {
		log.Printf(msg, args...)
	}
}
