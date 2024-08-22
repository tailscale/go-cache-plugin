// Program gocache implements the experimental GOCACHEPROG protocol over an S3
// bucket, for use in builder and CI workers.
package main

import (
	"cmp"
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/creachadair/command"
	"github.com/creachadair/flax"
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
				Name:     "serve",
				Help:     `Run a cache server.`,
				SetFlags: command.Flags(flax.MustBind, &remoteFlags),
				Run:      command.Adapt(runRemote),
			},
			{
				Name:  "connect",
				Usage: "<socket-path>",
				Help:  `Connect to a remote cache server.`,
				Run:   command.Adapt(runConnect),
			},
			command.HelpCommand([]command.HelpTopic{{
				Name: "environment",
				Help: `Environment variables understood by this program.

To make it easier to configure this tool for multiple workflows, most of the
settings can be set via environment variables as well as flags.

    Flag              Variable               Format    Default
    --cache-dir       GOCACHE_DIR            string    (required)
    --bucket          GOCACHE_S3_BUCKET      string    (required)
    --region          GOCACHE_S3_REGION      string    based on bucket
    --prefix          GOCACHE_KEY_PREFIX     string    ""
    --min-upload-size GOCACHE_MIN_SIZE       int64     0
    -c                GOCACHE_CONCURRENCY    int       runtime.NumCPU
    -m                GOCACHE_METRICS        bool      false
    -x                GOCACHE_EXPIRY         duration  0
    -u                GOCACHE_S3_CONCURRENCY duration  runtime.NumCPU
    -v                GOCACHE_VERBOSE        bool      false
    --debug           GOCACHE_DEBUG          bool      false
`,
			}}),
			command.VersionCommand(),
		},
	}
	env := root.NewEnv(nil).MergeFlags(true)
	command.RunOrFail(env, os.Args[1:])
}

// getBucketRegion reports the specified region for the given bucket.
// if the --region flag was set, that value is returned without error.
// Otherwise, it queries the GetBucketLocation API.
func getBucketRegion(ctx context.Context, bucket string) (string, error) {
	if flags.S3Region != "" {
		return flags.S3Region, nil
	}

	// The default AWS region, which we use for resolving the bucket location
	// and also serves as the fallback if the API reports an empty region name.
	// The API returns "" for buckets in this region for historical reasons.
	const defaultRegion = "us-east-1"

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(defaultRegion))
	if err != nil {
		return "", err
	}
	cli := s3.NewFromConfig(cfg)
	loc, err := cli.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: &bucket})
	if err != nil {
		return "", err
	}
	return cmp.Or(string(loc.LocationConstraint), defaultRegion), nil
}

// vprintf acts as log.Printf if the --verbose flag is set; otherwise it
// discards its input.
func vprintf(msg string, args ...any) {
	if flags.Verbose {
		log.Printf(msg, args...)
	}
}
