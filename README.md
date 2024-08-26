# go-cache-plugin

[![GoDoc](https://img.shields.io/static/v1?label=godoc&message=reference&color=lightgrey)](https://pkg.go.dev/github.com/tailscale/go-cache-plugin)
[![CI](https://github.com/tailscale/go-cache-plugin/actions/workflows/go-presubmit.yml/badge.svg?event=push&branch=main)](https://github.com/tailscale/go-cache-plugin/actions/workflows/go-presubmit.yml)

This repository defines a tool implementing a `GOCACHEPROG` plugin backed by Amazon S3.

## Installation

```shell
go install github.com/tailscale/go-cache-plugin/cmd/go-cache-plugin@latest
```

## Usage Outline

```shell
export GOCACHEPROG="go-cache-plugin --cache-dir=/tmp/gocache --bucket=some-s3-bucket"
go test ./...
```

Using the plugin requires a Go toolchain built with `GOEXPERIMENT=cacheprog` enabled.
However, you do not need the experiment enabled to build the plugin itself.

## Discussion

The `go-cache-plugin` program supports two modes of operation:

1. **Direct mode**: The program is invoked directly by the Go toolchain as a
   subprocess, and exits when the toolchain execution ends.

   This is the default mode of operation, and requires no additional setup.

2. **Server mode**: The program runs as a separate process and the Go toolchain
   communicates with it over a Unix-domain socket.

   This mode requires the server to be started up ahead of time, but makes the
   configuration for the toolchain simpler. This mode also permits running an
   in-process module and sum database proxy.

### Server Mode

To run in server mode, use the `serve` subcommand:

```sh
# N.B.: The --socket flag is required.
go-cache-plugin serve \
   --socket=/tmp/gocache.sock \
   --cache-dir=/tmp/gocache \
   --bucket=some-s3-bucket
```

To connect to a server running in this mode, use the `connect` subcommand:

```sh
# Use the same socket path given to the server's --socket flag.
export GOCACHEPROG="go-cache-plugin connect /tmp/gocache.sock"
go build ./...
```

The `connect` command just bridges the socket to stdin/stdout, which is how the
Go toolchain expects to talk to the plugin.

### Running a Module Proxy

To enable a caching module proxy, use the `--modproxy` flag to `serve`.  The
module proxy uses HTTP, not the plugin interface:

```sh
go-cache-plugin serve \
   --socket=/tmp/gocache.sock \
   --modproxy=localhost:5970 \
   --cache-dir=/tmp/gocache \
   # ... other flags
```

To tell the Go toolchain about the proxy, set:

```sh
export GOPROXY=http://localhost:5970   # use the --modproxy address
```

If you want to also proxy queries to `sum.golang.org`, also add:

```sh
export GOSUMDB='sum.golang.org http://locahost:5970/sumdb/sum.golang.org'
```

## References

- [Cache plugin proposal](https://github.com/golang/go/issues/59719)
- [Module proxy documentation](proxy.golang.org)
