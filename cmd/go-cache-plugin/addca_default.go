// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package main

import (
	"errors"
	"log"

	"github.com/creachadair/atomicfile"
	"github.com/creachadair/command"
	"github.com/creachadair/tlsutil"
)

func installSigningCert(env *command.Env, cert tlsutil.Certificate) error {
	const certFile = "revproxy-ca.crt"
	if err := atomicfile.WriteData(certFile, cert.CertPEM(), 0644); err != nil {
		log.Printf("WARNING: Unable to write cert file: %v", err)
	} else {
		log.Printf("Wrote signing cert to %s", certFile)
	}
	// TODO(creachadair): Maybe crib some other cases from mkcert, if we need
	// them, for example:
	// https://github.com/FiloSottile/mkcert/blob/master/truststore_darwin.go

	return errors.New("unable to install a certificate on this system")
}
