package update

import (
	"errors"
	"os"
	"syscall"
)

// ErrInProgress means another update of this binary is running.
var ErrInProgress = errors.New("another update of this binary is running")

// lock takes the update lock for the binary at path. The kernel drops it when
// the process ends, so a killed update cannot leave one behind. The empty lock
// file stays: removing it would let two updates lock two different files.
func lock(path string) (release func(), err error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.Join(ErrPermission, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrInProgress
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
