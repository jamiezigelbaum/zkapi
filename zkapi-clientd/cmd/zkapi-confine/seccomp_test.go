//go:build linux && amd64

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

// Golden captured by running the reviewed afdb5406 prototype's dumpfilter
// with the pinned Go toolchain and all prototype switches unset.
func TestFilterGolden(t *testing.T) {
	var got strings.Builder
	for i, p := range []profile{profileStage0, profileStage1, profileStage2} {
		prog, err := buildFilter(p)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&got, "# profile %d, %d instructions\n", i, len(prog))
		for n, f := range prog {
			fmt.Fprintf(&got, "%3d: code=%#04x jt=%d jf=%d k=%#x\n", n, f.Code, f.Jt, f.Jf, f.K)
			if f.Code == bpfJeqK || f.Code == bpfJgeK {
				for _, off := range []uint8{f.Jt, f.Jf} {
					if n+1+int(off) >= len(prog) {
						t.Fatalf("profile %d jump %d out of program", i, n)
					}
				}
			}
		}
	}
	want, err := os.ReadFile("testdata/seccomp-amd64.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatal("seccomp program changed from reviewed prototype golden")
	}
}

func TestAssemblerRejectsInvalidJumps(t *testing.T) {
	for _, kind := range []string{"missing", "backward", "256"} {
		t.Run(kind, func(t *testing.T) {
			a := newAsm()
			if kind == "backward" {
				a.label("end")
			}
			a.jump(bpfJeqK, 0, "end", "")
			if kind == "256" {
				for i := 0; i < 256; i++ {
					a.ret(0)
				}
				a.label("end")
			}
			if _, err := a.resolve(); err == nil {
				t.Fatal("invalid jump accepted")
			}
		})
	}
	a := newAsm()
	a.jump(bpfJeqK, 0, "end", "")
	for i := 0; i < 255; i++ {
		a.ret(0)
	}
	a.label("end")
	a.ret(0)
	if p, err := a.resolve(); err != nil || p[0].Jt != 255 {
		t.Fatalf("255-byte jump: %v %v", p, err)
	}
}

// Minimal independent classic-BPF interpreter: policy tests state expected
// kernel decisions, independent of builder labels and syscall map.
func filterResult(t *testing.T, prog []sockFilter, nr, arch uint32, args ...uint64) uint32 {
	t.Helper()
	words := map[uint32]uint32{0: nr, 4: arch}
	for i, v := range args {
		words[16+uint32(i)*8] = uint32(v)
		words[20+uint32(i)*8] = uint32(v >> 32)
	}
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		f := prog[pc]
		switch f.Code {
		case 0x20:
			acc = words[f.K]
		case 0x54:
			acc &= f.K
		case 0x15:
			if acc == f.K {
				pc += int(f.Jt)
			} else {
				pc += int(f.Jf)
			}
		case 0x35:
			if acc >= f.K {
				pc += int(f.Jt)
			} else {
				pc += int(f.Jf)
			}
		case 0x06:
			return f.K
		default:
			t.Fatalf("unsupported opcode %x", f.Code)
		}
	}
	t.Fatal("program fell through")
	return 0
}

func TestFilterSecurityDecisions(t *testing.T) {
	const allow = 0x7fff0000
	const deny = 0x50001
	const arch = 0xc000003e
	for _, p := range []profile{profileStage0, profileStage1, profileStage2} {
		prog, err := buildFilter(p)
		if err != nil {
			t.Fatal(err)
		}
		check := func(name string, nr uint32, want uint32, args ...uint64) {
			t.Helper()
			if got := filterResult(t, prog, nr, arch, args...); got != want {
				t.Fatalf("profile %d %s: %x want %x", p, name, got, want)
			}
		}
		// PR_SET_PDEATHSIG is an int: high option bits do not bypass its guard.
		for _, opt := range []uint64{1, 1 + (1 << 32)} {
			for sig := uint64(0); sig <= 64; sig++ {
				want := uint32(deny)
				if sig == 9 {
					want = allow
				}
				check("pdeathsig", 157, want, opt, sig)
			}
		}
		check("other prctl unchanged", 157, allow, 38, 1)
		check("clone3 libc fallback", 435, 0x50000|uint32(syscall.ENOSYS))
		for _, flag := range []uint64{0x80, 0x20000, 0x2000000, 0x4000000, 0x8000000, 0x10000000, 0x20000000, 0x40000000} {
			check("namespace clone", 56, deny, flag)
		}
		check("ordinary clone", 56, allow, 0x100|0x200|17)
		for _, nr := range []uint32{101, 310, 311, 438, 312, 272, 308, 425, 426, 427, 165, 166, 155, 161, 442, 429, 428, 430, 431, 432, 433, 304, 321, 298, 323, 246, 320, 175, 313, 176, 169, 250, 248, 249, 167, 168, 163} {
			check("denied syscall", nr, deny)
		}
		check("x32", 0x40000001, deny)
		if got := filterResult(t, prog, 1, 0xc00000b7); got != 0x80000000 {
			t.Fatal("foreign architecture did not kill")
		}
		check("tcp", 41, allow, 2, 1, 6)
		check("tcp default protocol", 41, allow, 2, 1|0x80000, 0)
		for _, args := range [][]uint64{{1, 1, 0}, {10, 1, 6}, {2, 2, 0}, {2, 3, 0}, {2, 1, 17}, {40, 1, 0}, {16, 3, 16}} {
			check("bad socket", 41, deny, args...)
		}
		want := uint32(allow)
		if p == profileStage0 {
			want = deny
		}
		check("stream pair", 53, want, 1, 1|0x80000, 0)
		for _, args := range [][]uint64{{1, 2, 0}, {1, 5, 0}, {2, 1, 0}, {1, 1, 6}} {
			check("bad pair", 53, deny, args...)
		}
	}
}

func TestProductionIgnoresPrototypeEnvironment(t *testing.T) {
	for _, name := range []string{"ZKAPI_CONFINE_PROTOTYPE_ABLATE", "ZKAPI_CONFINE_PROTOTYPE_FAULT", "ZKAPI_CONFINE_PROTOTYPE_ABI", "ZKAPI_CONFINE_PROTOTYPE_PAIR_ALLOW"} {
		t.Setenv(name, "seccomp,dgram,seqpacket,0")
	}
	TestFilterGolden(t)
}
