//go:build !linux

package bklite_script

func currentUIDThreadCount() (int, bool) { return 0, false }
