//go:build !unix

package debug

func supportsUnixSockets() bool { return false }
