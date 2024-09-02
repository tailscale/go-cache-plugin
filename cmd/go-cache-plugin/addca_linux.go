// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/creachadair/command"
	"github.com/creachadair/tlsutil"
	"golang.org/x/sys/unix"
)

func installSigningCert(env *command.Env, cert tlsutil.Certificate) error {
	const ubuntuCertFile = "/etc/ssl/certs/ca-certificates.crt"
	return lockAndAppend(ubuntuCertFile, cert.CertPEM())
}

// lockAndAppend acquires an exclusive advisory lock on path, if possible, and
// appends data to the end of it. It reports an error if path does not exist,
// or if the lock could not be acquired. The lock is automatically released
// before returning.
func lockAndAppend(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	fd := int(f.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		f.Close()
		return fmt.Errorf("lock: %w", err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	_, werr := f.Write(data)
	cerr := f.Close()
	return errors.Join(werr, cerr)
}
