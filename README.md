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

## References

- [Cache plugin proposal](https://github.com/golang/go/issues/59719)
