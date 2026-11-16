package main

import (
	"errors"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"unsafe"
)

// Terminal echo control for the --password prompt, on the standard library
// alone.
//
// The ioctl request numbers are picked at run time instead of through build
// tags on purpose: the build scripts compile an explicit file list
// (go build ../*.go), and in that mode the go tool ignores build constraints,
// so per-OS files would all be compiled together.

func termiosRequests() (get uintptr, set uintptr, ok bool) {
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "386", "amd64", "arm", "arm64", "riscv64", "s390x", "loong64":
			return 0x5401, 0x5402, true // TCGETS, TCSETS
		case "mips", "mipsle", "mips64", "mips64le":
			return 0x540d, 0x540e, true
		case "ppc64", "ppc64le":
			return 0x402c7413, 0x802c7414, true
		}
	case "freebsd", "netbsd", "openbsd", "dragonfly":
		return 0x402c7413, 0x802c7414, true // TIOCGETA, TIOCSETA
	case "darwin":
		return 0x40487413, 0x80487414, true
	}

	return 0, 0, false
}

func getTermios(fd uintptr, t *syscall.Termios) error {
	get, _, ok := termiosRequests()
	if ok == false {
		return errors.New("terminal control is not supported on " + runtime.GOOS + "/" + runtime.GOARCH)
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, get, uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

func setTermios(fd uintptr, t *syscall.Termios) error {
	_, set, ok := termiosRequests()
	if ok == false {
		return errors.New("terminal control is not supported on " + runtime.GOOS + "/" + runtime.GOARCH)
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, set, uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

// isTerminal reports whether f is a terminal a person can type into.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0 && info.Mode()&os.ModeDevice != 0
}

// disableEcho stops the terminal from showing what is typed and returns the
// function that puts it back. Line editing stays on, so backspace works, and
// the newline of Enter is still echoed. If the process is interrupted while
// echo is off, the terminal is restored before it exits.
func disableEcho(f *os.File) (restore func(), err error) {
	fd := f.Fd()

	var saved syscall.Termios
	if err = getTermios(fd, &saved); err != nil {
		return nil, err
	}

	quiet := saved
	quiet.Lflag &^= syscall.ECHO
	quiet.Lflag |= syscall.ICANON | syscall.ISIG | syscall.ECHONL

	if err = setTermios(fd, &quiet); err != nil {
		return nil, err
	}

	sig := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)

	go func() {
		select {
		case <-sig:
			setTermios(fd, &saved)
			os.Stdout.WriteString("\n")
			os.Exit(1)
		case <-done:
		}
	}()

	return func() {
		signal.Stop(sig)
		close(done)
		setTermios(fd, &saved)
	}, nil
}
