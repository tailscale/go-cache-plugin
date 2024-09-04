// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package s3util_test

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/tailscale/go-cache-plugin/lib/s3util"
)

func TestETagReader(t *testing.T) {
	const testInput = "the once and future kitten"

	want := md5.Sum([]byte(testInput))
	t.Logf("MD5(%q) = %x", testInput, want)

	r := s3util.NewETagReader(strings.NewReader(testInput))

	nr, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatalf("Copy failed; %v", err)
	} else if nr != int64(len(testInput)) {
		t.Errorf("Copied %d bytes, want %d", nr, len(testInput))
	}

	etag := r.ETag()
	t.Logf("Got etag %s for input %q", etag, testInput)

	got, err := hex.DecodeString(r.ETag())
	if err != nil {
		t.Fatalf("Result is not valid hex: %s", r.ETag())
	}
	if !bytes.Equal(got, want[:]) {
		t.Errorf("Wrong result: got %x, want %x", got, want)
	}
}
