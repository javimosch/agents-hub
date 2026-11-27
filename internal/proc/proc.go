// Package proc runs a subprocess so that its deadline actually holds.
//
// exec.CommandContext kills only the process it started. A grandchild then
// lives on, and while it holds the output pipe open, Wait does not return.
package proc

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// grace is how long output may keep arriving once a command has exited or
// been killed. After it the pipes are closed, whoever still holds them.
const grace = time.Second

// Run runs a command built with exec.CommandContext in its own process group.
// When the context ends, the whole group is killed and killed is true.
//
// A command that exits by itself has finished, even if something it left
// behind (a daemon) still holds its output: that process is not killed.
func Run(cmd *exec.Cmd) (killed bool, err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		killed = err == nil
		return err
	}
	cmd.WaitDelay = grace

	stop := forwardSignals()
	defer stop.stop()
	if err = cmd.Start(); err != nil {
		return killed, err
	}
	stop.to(cmd.Process.Pid)
	err = cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		// Only reported when the command itself exited successfully.
		err = nil
	}
	return killed, err
}

// forwarder passes this process's termination signals on to a command's group.
// A group of its own no longer receives what is sent to ours, which is how
// both `job stop` and Ctrl-C reach a command.
type forwarder struct {
	signals chan os.Signal
	done    chan struct{}
	running sync.WaitGroup
}

// forwardSignals starts listening before the command does, so that a signal
// arriving during Start is held rather than lost.
func forwardSignals() *forwarder {
	f := &forwarder{signals: make(chan os.Signal, 1), done: make(chan struct{})}
	for _, sig := range []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		// Listening to an ignored signal (nohup) would un-ignore it.
		if !signal.Ignored(sig) {
			signal.Notify(f.signals, sig)
		}
	}
	return f
}

func (f *forwarder) to(pid int) {
	f.running.Add(1)
	go func() {
		defer f.running.Done()
		select {
		case sig := <-f.signals:
			f.pass(pid, sig.(syscall.Signal))
		case <-f.done:
		}
	}()
}

// pass sends the signal to the group, then raises it again here with our
// listener removed, so this process reacts exactly as it would have.
func (f *forwarder) pass(pid int, sig syscall.Signal) {
	_ = syscall.Kill(-pid, sig)
	signal.Stop(f.signals)
	_ = syscall.Kill(os.Getpid(), sig)
}

// stop ends the listening. A signal caught but not yet passed on still has to
// reach this process: swallowing it would ignore a Ctrl-C.
func (f *forwarder) stop() {
	signal.Stop(f.signals)
	close(f.done)
	f.running.Wait()
	select {
	case sig := <-f.signals:
		_ = syscall.Kill(os.Getpid(), sig.(syscall.Signal))
	default:
	}
}
