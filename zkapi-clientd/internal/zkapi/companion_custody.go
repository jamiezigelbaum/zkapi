package zkapi

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// CompanionCustody allows a managed wallet bridge connection only when the
// exact supervised child owns the server side of that already-connected TCP
// socket. No HTTP bytes are available to the peer until this proof succeeds.
type CompanionCustody struct {
	mu     sync.Mutex
	active *companionCustodyProcess
}

type companionCustodyProcess struct {
	pid      int
	identity companionProcessIdentity
}

type companionProcessIdentity interface {
	exited() (bool, error)
	close()
}

// CompanionCustodySupported reports whether this build can bind a connected
// socket to the lifetime of a supervised companion process.
func CompanionCustodySupported() bool { return companionCustodyPlatformSupported }

// NewCompanionCustody creates inactive custody. Activate must be called only
// after the supervised child has started successfully.
func NewCompanionCustody() (*CompanionCustody, error) {
	if !CompanionCustodySupported() {
		return nil, errors.New("managed companion custody is not supported on this platform")
	}
	return &CompanionCustody{}, nil
}

// Activate binds this custody instance to one exact live child lifetime.
func (c *CompanionCustody) Activate(pid int) error {
	if c == nil || pid <= 0 {
		return errors.New("managed companion custody requires a started child")
	}
	identity, err := newCompanionProcessIdentity(pid)
	if err != nil {
		return errors.New("could not establish managed companion process custody")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		identity.close()
		return errors.New("managed companion custody is already active")
	}
	c.active = &companionCustodyProcess{pid: pid, identity: identity}
	return nil
}

// Retire removes authority for pid. It is safe to call more than once. The
// process identity guard remains active until retirement, so a reaped PID
// cannot silently transfer custody to a later process.
func (c *CompanionCustody) Retire(pid int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.pid != pid {
		return
	}
	c.active.identity.close()
	c.active = nil
}

// IsActive reports whether the exact child lifetime is still held. It is used
// for in-process supervised startup readiness so no API credential is sent to
// an unverified process occupying the daemon's own listen port.
func (c *CompanionCustody) IsActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return false
	}
	exited, err := c.active.identity.exited()
	return err == nil && !exited
}

// DialContext is installed directly on the wallet-only http.Transport. It
// dials without sending application bytes, proves that the supervised child
// owns the reverse endpoint of this exact connection, then hands this same
// socket to net/http. A listener swap cannot replace an established socket.
func (c *CompanionCustody) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if c == nil {
		return nil, errors.New("managed companion custody is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return nil, errors.New("managed companion custody is inactive")
	}
	exited, err := c.active.identity.exited()
	if err != nil || exited {
		return nil, errors.New("managed companion is no longer in custody")
	}
	connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	fail := func() (net.Conn, error) {
		_ = connection.Close()
		return nil, errors.New("wallet connection owner could not be verified")
	}
	local, localOK := connection.LocalAddr().(*net.TCPAddr)
	remote, remoteOK := connection.RemoteAddr().(*net.TCPAddr)
	if !localOK || !remoteOK {
		return fail()
	}
	owned, err := companionOwnsConnectedSocket(ctx, c.active.pid, local, remote)
	if err != nil || !owned {
		return fail()
	}
	exited, err = c.active.identity.exited()
	if err != nil || exited {
		return fail()
	}
	return connection, nil
}
