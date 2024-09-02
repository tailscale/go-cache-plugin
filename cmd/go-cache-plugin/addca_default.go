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
	return errors.New("unable to install a certificate on this system")
}
