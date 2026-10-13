//go:build !darwin && !linux

package zkapi

import (
	"context"
	"errors"
	"net"
)

const companionCustodyPlatformSupported = false

type unsupportedCompanionProcessIdentity struct{}

func newCompanionProcessIdentity(int) (companionProcessIdentity, error) {
	return nil, errors.New("managed companion custody is not supported")
}
func (*unsupportedCompanionProcessIdentity) exited() (bool, error) { return true, nil }
func (*unsupportedCompanionProcessIdentity) close()                {}
func companionOwnsConnectedSocket(context.Context, int, *net.TCPAddr, *net.TCPAddr) (bool, error) {
	return false, errors.New("managed companion custody is not supported")
}
