//go:build !linux && !darwin

package keepawake

func startPlatform() func() { return nil }
