// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"os/exec"

	"github.com/creachadair/command"
	"github.com/creachadair/tlsutil"
)

func installSigningCert(env *command.Env, cert tlsutil.Certificate) error {
	tf, err := os.CreateTemp("", "addca.*")
	if err != nil {
		return err
	}
	defer os.Remove(tf.Name())
	defer tf.Close()

	if _, err := tf.Write(cert.CertPEM()); err != nil {
		return err
	} else if err := tf.Close(); err != nil {
		return err
	}

	const systemKeychain = "/Library/Keychains/System.keychain"
	return sudo("security", "add-trusted-cert", "-d", "-k", systemKeychain, tf.Name())
}

func sudo(args ...string) error {
	cmd := exec.Command("sudo", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
