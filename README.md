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
   communicates with it over a local socket.

   This mode requires the server to be started up ahead of time, but makes the
   configuration for the toolchain simpler. This mode also permits running an
   in-process module and sum database proxy.

### Server Mode

To run in server mode, use the `serve` subcommand:

```sh
# N.B.: The --plugin flag is required.
go-cache-plugin serve \
   --plugin=5930 \
   --cache-dir=/tmp/gocache \
   --bucket=some-s3-bucket
```

To connect to a server running in this mode, use the `connect` subcommand:

```sh
# Use the same port given to the server's --plugin flag.
# Mnemonic: 5930 == (Go) (C)ache (P)lugin
export GOCACHEPROG="go-cache-plugin connect 5930
go build ./...
```

The `connect` command just bridges the socket to stdin/stdout, which is how the
Go toolchain expects to talk to the plugin.

### Running a Module Proxy

To enable a caching module proxy, use the `--modproxy` flag to `serve`.  The
module proxy uses HTTP, not the plugin interface, use `--http` to set the address:

```sh
go-cache-plugin serve \
   --plugin=5930 \
   --http=localhost:5970 --modproxy \
   --cache-dir=/tmp/gocache \
   # ... other flags
```

To tell the Go toolchain about the proxy, set:

```sh
export GOPROXY=http://localhost:5970/mod   # use the --http address
```

If you want to also proxy queries to `sum.golang.org`, also add:

```sh
export GOSUMDB='sum.golang.org http://locahost:5970/mod/sumdb/sum.golang.org'
```

## References

- [Cache plugin protocol (proposal)](https://github.com/golang/go/issues/59719)
- [Cache plugin library](https://github.com/creachadair/gocache)
- [Go module proxy documentation](https://proxy.golang.org)
