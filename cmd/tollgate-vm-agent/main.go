package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	vsockPort    = 52
	defaultShell = "/bin/sh"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("tollgate-vm-agent starting, vsock port %d", vsockPort)

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		log.Fatalf("AF_VSOCK socket: %v", err)
	}
	defer unix.Close(fd)

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: vsockPort,
	}
	if err := unix.Bind(fd, sa); err != nil {
		log.Fatalf("vsock bind port %d: %v", vsockPort, err)
	}
	if err := unix.Listen(fd, 4); err != nil {
		log.Fatalf("vsock listen: %v", err)
	}

	go reapZombies()

	for {
		connFd, sa, err := unix.Accept(fd)
		if err != nil {
			if isTemporaryErr(err) {
				continue
			}
			log.Printf("accept: %v", err)
			continue
		}
		log.Printf("vsock connection accepted")
		conn := os.NewFile(uintptr(connFd), fmt.Sprintf("vsock-conn-%v", sa))
		go handleSession(conn)
	}
}

func handleSession(conn *os.File) {
	defer conn.Close()

	env := buildEnv()
	shell := os.Getenv("TOLLGATE_SHELL")
	if shell == "" {
		shell = defaultShell
	}

	cmd := exec.Command(shell)
	cmd.Env = env
	cmd.Stdin = conn
	cmd.Stdout = conn
	cmd.Stderr = conn

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(conn, "\r\ntollgate-vm-agent: shell start failed: %v\r\n", err)
		return
	}

	log.Printf("shell PID=%d (%s)", cmd.Process.Pid, shell)

	cmd.Wait()
	log.Printf("shell PID=%d exited", cmd.Process.Pid)
}

func buildEnv() []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm-256color",
		"HOME=/root",
	}
	for _, key := range []string{
		"TOLLGATE_AMOUNT", "TOLLGATE_DURATION", "TOLLGATE_MINT",
		"TOLLGATE_GUEST", "TOLLGATE_SESSION_START",
	} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func reapZombies() {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGCHLD)
	for range sigs {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}

func isTemporaryErr(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.EINTR || errno == syscall.ECONNABORTED
	}
	return false
}

func init() {
	if v := os.Getenv("TOLLGATE_VSOCK_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			log.Printf("overriding vsock port from env: %d", p)
		}
	}
}
