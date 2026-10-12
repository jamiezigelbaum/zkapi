//go:build linux && amd64 && qualification && qualification_adversary

package main

import "syscall"

// Compile-time adversarial init fixture; no runtime fault selector.
func qualificationInit() {
	// An adversarial init stand-in: a compromised stage1 trying to outlive
	// its parent. It reports what the filter let it do; the matrix then
	// kills stage0 and requires that nothing survives.
	res := map[string]any{"event": "init_escape"}
	_, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetPdeathsig, uintptr(syscall.SIGKILL), 0)
	res["keepPdeathsig"] = errName(errnoOrNil(e))
	_, _, e = syscall.RawSyscall(syscall.SYS_PRCTL, prSetPdeathsig, uintptr(syscall.SIGTERM), 0)
	res["softenPdeathsig"] = errName(errnoOrNil(e))
	_, _, e = syscall.RawSyscall(syscall.SYS_PRCTL, prSetPdeathsig, 0, 0)
	res["clearPdeathsig"] = errName(errnoOrNil(e))
	_, serr := syscall.Setsid()
	res["setsid"] = errName(serr)
	emitFn(res)
}
