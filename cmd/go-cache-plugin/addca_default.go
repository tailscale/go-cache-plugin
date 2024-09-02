// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package main

import (
	"errors"

	"github.com/creachadair/command"
	"github.com/creachadair/tlsutil"
)

func installSigningCert(env *command.Env, cert tlsutil.Certificate) error {
	// TODO(creachadair): Maybe crib some other cases from mkcert, if we need
	// them, for example:
	// https://github.com/FiloSottile/mkcert/blob/master/truststore_darwin.go

	return errors.New("unable to install a certificate on this system")
}
