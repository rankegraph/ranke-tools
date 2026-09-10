//go:build !linux

// package: main (ranke-git) / sysprocattr_other
// type:    platform
// job:     the non-Linux stand-in for sysprocattr_linux.go's Pdeathsig
// limits:  no death-signal backstop here; the live-server test relies on t.Cleanup alone
package main

import "syscall"

func deathSigAttr() *syscall.SysProcAttr {
	return nil
}
