// +build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestReapChildren(t *testing.T) {
	sigc := make(chan os.Signal, 1)
	done := make(chan bool)
	signal.Notify(sigc, syscall.SIGCHLD)

	go func() {
		reapChildren(mainCtx, sigc)
		close(done)
	}()

	// Start a process
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("ERROR: %v", err)
	}

	// Kill the process, but do not call Wait()
	childPid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("ERROR: %v", err)
	}

	// Give reapChildren() a brief period to remove the orphaned process
	time.Sleep(1 * time.Second)

	// This is a fairly crude way of checking whether the process was cleaned
	// up, but seems to work well enough for a very crude unit test.
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", childPid)); !os.IsNotExist(err) {
		t.Fatalf("ERROR: process %d still exists, but should not.", childPid)
	}

	// Cancel the context
	mainCancel()
	<-done
}
