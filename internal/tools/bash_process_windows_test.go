//go:build windows

package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWindowsCommandTreeHelper(t *testing.T) {
	role := os.Getenv("GAX_TEST_TREE_ROLE")
	if role == "" {
		return
	}
	if role == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsCommandTreeHelper$")
		child.Env = append(os.Environ(), "GAX_TEST_TREE_ROLE=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stdout, err)
			os.Exit(2)
		}
	} else {
		fmt.Fprintln(os.Stdout, os.Getpid())
	}
	<-time.NewTimer(time.Hour).C
}

func TestWindowsCommandTree(t *testing.T) {
	for _, method := range []string{"kill", "close"} {
		t.Run(method, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsCommandTreeHelper$")
			cmd.Env = append(os.Environ(), "GAX_TEST_TREE_ROLE=parent")
			cmd.Stdout = writer
			process, err := startCommandProcess(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer process.close()
			_ = writer.Close()
			line := make(chan string, 1)
			go func() { text, _ := bufio.NewReader(reader).ReadString('\n'); line <- text }()
			var pid int
			select {
			case text := <-line:
				pid, err = strconv.Atoi(strings.TrimSpace(text))
				if err != nil {
					t.Fatalf("child PID = %q, want integer", text)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("child did not start")
			}
			child, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.CloseHandle(child)
			if method == "kill" {
				if !process.kill(cmd, syscall.SIGKILL) {
					t.Fatal("kill(process tree) = false, want true")
				}
			} else {
				process.close()
			}
			result, err := syscall.WaitForSingleObject(child, 5000)
			if err != nil || result != syscall.WAIT_OBJECT_0 {
				t.Errorf("wait(grandchild) = %v,%v, want terminated", result, err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("parent process did not stop")
			}
		})
	}
}
