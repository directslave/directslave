package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// The control file is written by three different processes: the daemon (API and
// dashboard), the "--domain" CLI, and anything an operator runs by hand. An
// in-process mutex cannot serialize that, so every reader and writer also takes
// an advisory lock. The lock lives in a file of its own because the control
// file itself is replaced by rename: a lock held on the replaced inode would
// protect nothing.
const controlLockTimeout = 15 * time.Second

var controlLockWarned sync.Once

func controlLockPath() string {
	return Config.named_conf + ".lock"
}

func controlWarn(message string, err error) {
	text := "NAMED_CONF: " + message
	if err != nil {
		text += " - " + err.Error()
	}

	if LogError != nil {
		LogError.Println(text)
		return
	}
	fmt.Fprintln(os.Stderr, text)
}

// lockControlFile blocks until the control file is free, or gives up after
// controlLockTimeout. A nil result means the lock could not be taken; the
// caller still proceeds, because refusing to serve or to answer an API call
// would be worse than the race the lock protects against.
func lockControlFile(exclusive bool) *os.File {
	file, err := os.OpenFile(controlLockPath(), os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		controlLockWarned.Do(func() {
			controlWarn("can't open the lock file "+controlLockPath()+", continuing without cross-process locking", err)
		})
		return nil
	}

	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}

	deadline := time.Now().Add(controlLockTimeout)
	for {
		err = syscall.Flock(int(file.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return file
		}

		if time.Now().After(deadline) {
			controlWarn("the control file stayed locked for "+controlLockTimeout.String()+", continuing without the lock", err)
			file.Close()
			return nil
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func unlockControlFile(file *os.File) {
	if file == nil {
		return
	}

	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

// CheckControlFileWritable verifies what a rewrite of the control file actually
// needs: the right to create a file in its directory AND the right to rename
// over it. Opening named_conf for append succeeds in cases where the rewrite
// still fails, which is why a failing --domain delete could look successful.
func CheckControlFileWritable() error {
	directory := filepath.Dir(Config.named_conf)

	tmp, err := os.CreateTemp(directory, ".directslave-check-*")
	if err != nil {
		return err
	}

	name := tmp.Name()
	defer os.Remove(name)

	if err := tmp.Close(); err != nil {
		return err
	}

	// Rename to a probe name, never over the real control file.
	probe := filepath.Join(directory, ".directslave-check-rename")
	if err := os.Rename(name, probe); err != nil {
		return err
	}

	return os.Remove(probe)
}

// CheckControlLock verifies the advisory lock file can be taken.
func CheckControlLock() error {
	file, err := os.OpenFile(controlLockPath(), os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// LeftoverTmpFile reports a ".tmp" file left behind by a pre-4.0.3 delete. Such
// a file used to be appended to and renamed over the control file, which
// brought deleted zones back.
func LeftoverTmpFile() (string, int64, bool) {
	path := Config.named_conf + ".tmp"

	info, err := os.Stat(path)
	if err != nil {
		return "", 0, false
	}

	return path, info.Size(), true
}

// writeControlFile replaces the control file in one atomic step. The previous
// implementation wrote in place, or appended to a fixed ".tmp" name that was
// never truncated: a leftover or concurrently written ".tmp" was then renamed
// over the control file and brought deleted zones back.
func writeControlFile(content []byte) error {
	target := Config.named_conf
	directory := filepath.Dir(target)

	tmp, err := os.CreateTemp(directory, ".directslave-conf-*")
	if err != nil {
		controlWarn("can't create a temporary file in "+directory, err)
		return err
	}

	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		controlWarn("can't write "+name, err)
		return err
	}

	// The rename below is atomic, but only fsync makes the new content survive
	// a power loss; without it a crash can leave an empty control file.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		controlWarn("can't flush "+name, err)
		return err
	}

	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		controlWarn("can't set permissions on "+name, err)
		return err
	}

	if err := tmp.Close(); err != nil {
		controlWarn("can't close "+name, err)
		return err
	}

	if err := os.Rename(name, target); err != nil {
		controlWarn("can't move "+name+" over "+target, err)
		return err
	}

	return nil
}
