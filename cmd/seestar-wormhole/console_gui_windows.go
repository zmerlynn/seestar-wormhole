//go:build gui

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"golang.org/x/sys/windows"
)

var attachedConsole *consoleControl

type consoleControl struct {
	requested chan struct{}
	finished  chan struct{}
	request   sync.Once
	finish    sync.Once
}

func newConsoleControl() *consoleControl {
	return &consoleControl{requested: make(chan struct{}), finished: make(chan struct{})}
}

func (c *consoleControl) handle(event uint32) uintptr {
	switch event {
	case windows.CTRL_C_EVENT, windows.CTRL_BREAK_EVENT:
		c.request.Do(func() { close(c.requested) })
	case windows.CTRL_CLOSE_EVENT, windows.CTRL_LOGOFF_EVENT, windows.CTRL_SHUTDOWN_EVENT:
		c.request.Do(func() { close(c.requested) })
		// Returning from these events terminates the process. Keep the handler
		// alive until the command's deferred release and log cleanup have joined,
		// using the same bound as the session owner. Windows may kill us sooner.
		timer := time.NewTimer(proxy.ShutdownTimeout())
		defer timer.Stop()
		select {
		case <-c.finished:
		case <-timer.C:
		}
	default:
		return 0
	}
	return 1
}

func notifyContext(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
	if attachedConsole == nil {
		return signal.NotifyContext(parent, signals...)
	}
	c := attachedConsole
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-c.requested:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		cancel()
		c.finish.Do(func() { close(c.finished) })
	}
}

// A windowsgui executable has no console by default. Arguments opt in to its
// parent's console; Explorer launches remain window-only. AttachConsole may
// replace process standard handles, so restore every valid inherited handle.
func init() {
	if len(os.Args) <= 1 {
		return
	}
	stdout, stdoutValid := inheritedOutput(windows.STD_OUTPUT_HANDLE)
	stderr, stderrValid := inheritedOutput(windows.STD_ERROR_HANDLE)
	attach := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	attached, _, _ := attach.Call(uintptr(^uint32(0))) // ATTACH_PARENT_PROCESS
	var handlerErr error
	if attached != 0 {
		// AttachConsole resets the handler table, including Go's runtime handler.
		// Register once, after successful attachment, without runtime internals.
		attachedConsole = newConsoleControl()
		setHandler := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
		ok, _, err := setHandler.Call(windows.NewCallback(attachedConsole.handle), 1)
		if ok == 0 {
			handlerErr = err
		}
	}
	restoreConsoleOutput(windows.STD_OUTPUT_HANDLE, stdout, stdoutValid, &os.Stdout)
	restoreConsoleOutput(windows.STD_ERROR_HANDLE, stderr, stderrValid, &os.Stderr)
	if handlerErr != nil {
		fmt.Fprintln(os.Stderr, "cannot install console control handler:", handlerErr)
		os.Exit(1)
	}
}

func inheritedOutput(id uint32) (windows.Handle, bool) {
	handle, err := windows.GetStdHandle(id)
	if err != nil || handle == 0 || handle == windows.InvalidHandle {
		return handle, false
	}
	_, err = windows.GetFileType(handle)
	return handle, err == nil
}

func restoreConsoleOutput(id uint32, handle windows.Handle, valid bool, output **os.File) {
	if valid {
		_ = windows.SetStdHandle(id, handle)
		return
	}
	file, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	if err := windows.SetStdHandle(id, windows.Handle(file.Fd())); err != nil {
		file.Close()
		return
	}
	*output = file
}
