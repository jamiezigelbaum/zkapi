//go:build !linux || !amd64

package main

import (
	"fmt"
	"os"
)

// No confinement code or architecture-specific syscall table is compiled here.
func main() {
	fmt.Println(`{"ok":false,"code":"unsupported_platform","detail":"zkapi-confine requires linux/amd64"}`)
	os.Exit(78)
}
