//go:build linux

package prompt

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Linux's generic termios ioctl numbers -- correct across every
// architecture this project targets (x86_64 VPS appliance; also
// verified on this dev sandbox's arm64). A few Linux architectures
// (notably PowerPC/sparc) use different values, but this project has no
// reason to ever run on those.
const (
	tcgets = 0x5401
	tcsets = 0x5402
)

// termios mirrors the Linux kernel's struct termios layout closely
// enough for the one thing this needs: toggling ECHO in c_lflag. Hand-
// rolled via a direct ioctl rather than a third-party terminal package
// (e.g. golang.org/x/term), consistent with this project's stdlib-only
// convention -- syscall is stdlib, x/term is not.
type termios struct {
	Iflag, Oflag, Cflag, Lflag uint32
	Line                       byte
	Cc                         [19]byte
	Ispeed, Ospeed             uint32
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// AskSecret reads one line from stdin with terminal echo disabled, for
// password/token entry. Restores the terminal's prior state afterward
// even on error.
func AskSecret(label string) (string, error) {
	fmt.Printf("%s: ", label)
	fd := os.Stdin.Fd()

	var oldState termios
	if err := ioctl(fd, tcgets, unsafe.Pointer(&oldState)); err != nil {
		return "", fmt.Errorf("prompt: get terminal state: %w", err)
	}
	newState := oldState
	newState.Lflag &^= syscall.ECHO
	if err := ioctl(fd, tcsets, unsafe.Pointer(&newState)); err != nil {
		return "", fmt.Errorf("prompt: disable terminal echo: %w", err)
	}
	defer ioctl(fd, tcsets, unsafe.Pointer(&oldState))

	line, err := stdin.ReadString('\n')
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("prompt: read secret: %w", err)
	}
	return trimNewline(line), nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
