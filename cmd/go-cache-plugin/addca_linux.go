// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"github.com/creachadair/command"
	"github.com/creachadair/tlsutil"
)

func installSigningCert(env *command.Env, cert tlsutil.Certificate) error {
	const ubuntuCertFile = "/etc/ssl/certs/ca-certificates.crt"
	return lockAndAppend(ubuntuCertFile, cert.CertPEM())
}
