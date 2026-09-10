//go:build linux

// package: main (ranke-git) / sysprocattr_linux
// type:    platform
// job:     Pdeathsig for the live-server test — the kernel signals server/run.sh's child
// the moment the forking thread exits, so a crashed test still stops it
// limits:  Linux only; syscall.SysProcAttr has no Pdeathsig field elsewhere (-> sysprocattr_other.go)
package main

import "syscall"

func deathSigAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
