//go:build linux

// Command workload is the process of every container the Engine and Compose
// integration tests run (#21, #28). The harness builds it statically and
// loads it into DinD Engines as the image dockyard-test/workload:1
// (testharness.WorkloadImage), so the tests need no image from Docker Hub.
//
//	workload serve [line...]      print lines (stdout; "err:" prefix to stderr), then block until SIGTERM
//	workload ready-after MS FILE  create FILE after MS milliseconds, then block
//	workload check FILE           exit 0 if FILE exists (health checks)
//	workload fail                 exit 1 (a health check that never passes)
//	workload exit CODE [MSG]      print MSG, exit with CODE
//	workload echo ARG...          print the arguments
//	workload cat FILE             print a file
//	workload write FILE TEXT      write TEXT to FILE and block
//	workload stdin                copy stdin to stdout
//	workload ttysize              print the terminal size, read a line, print it again
//	workload listeners            print every listening TCP/UDP/unix socket of the network namespace
//	workload tick MS              print a counter every MS milliseconds until SIGTERM
//	workload listen PORT          listen on TCP PORT and block (positive control for listeners)
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: workload COMMAND [ARG...]")
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "serve":
		for _, l := range args {
			if s, ok := strings.CutPrefix(l, "err:"); ok {
				fmt.Fprintln(os.Stderr, s)
			} else {
				fmt.Println(l)
			}
		}
		block()
	case "ready-after":
		need(args, 2)
		ms, _ := strconv.Atoi(args[0])
		go func() {
			time.Sleep(time.Duration(ms) * time.Millisecond)
			if err := os.WriteFile(args[1], []byte("ready\n"), 0o644); err != nil {
				fatal(err.Error())
			}
			fmt.Println("ready")
		}()
		block()
	case "check":
		need(args, 1)
		if _, err := os.Stat(args[0]); err != nil {
			os.Exit(1)
		}
	case "fail":
		os.Exit(1)
	case "exit":
		need(args, 1)
		code, _ := strconv.Atoi(args[0])
		if len(args) > 1 {
			fmt.Println(strings.Join(args[1:], " "))
		}
		os.Exit(code)
	case "echo":
		fmt.Println(strings.Join(args, " "))
	case "cat":
		need(args, 1)
		b, err := os.ReadFile(args[0])
		if err != nil {
			fatal(err.Error())
		}
		_, _ = os.Stdout.Write(b)
	case "write":
		need(args, 2)
		if err := os.WriteFile(args[0], []byte(args[1]), 0o644); err != nil {
			fatal(err.Error())
		}
		fmt.Println("written")
		block()
	case "stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "ttysize":
		printSize()
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		printSize()
	case "listeners":
		listeners()
	case "listen":
		need(args, 1)
		ln, err := net.Listen("tcp", ":"+args[0])
		if err != nil {
			fatal(err.Error())
		}
		defer ln.Close()
		fmt.Println("listening")
		block()
	case "tick":
		need(args, 1)
		ms, _ := strconv.Atoi(args[0])
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		t := time.NewTicker(time.Duration(ms) * time.Millisecond)
		for i := 1; ; i++ {
			select {
			case <-t.C:
				fmt.Println("tick", i)
			case <-sig:
				return
			}
		}
	default:
		fatal("unknown command " + os.Args[1])
	}
}

func need(args []string, n int) {
	if len(args) < n {
		fatal(fmt.Sprintf("%s needs %d arguments", os.Args[1], n))
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "workload:", msg)
	os.Exit(2)
}

// block waits for SIGTERM/SIGINT and exits 0 (a well-behaved service).
func block() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	os.Exit(0)
}

func printSize() {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		fmt.Println("size error", errno)
		return
	}
	fmt.Printf("size %d %d\n", ws.Row, ws.Col)
}

// listeners prints listening sockets from /proc/net: TCP in state LISTEN
// (0A), bound UDP sockets (state 07) and unix sockets with __SO_ACCEPTCON.
// It prints "none" when there are none.
func listeners() {
	found := 0
	for _, f := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := os.ReadFile("/proc/net/" + f)
		if err != nil {
			fatal(err.Error())
		}
		for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			fields := strings.Fields(line)
			if i == 0 || len(fields) < 4 {
				continue
			}
			state := fields[3]
			if (strings.HasPrefix(f, "tcp") && state == "0A") || (strings.HasPrefix(f, "udp") && state == "07") {
				fmt.Println(f, fields[1])
				found++
			}
		}
	}
	b, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		fatal(err.Error())
	}
	for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) < 4 {
			continue
		}
		if flags, err := strconv.ParseUint(fields[3], 16, 32); err == nil && flags&0x10000 != 0 {
			path := ""
			if len(fields) >= 8 {
				path = fields[7]
			}
			fmt.Println("unix", path)
			found++
		}
	}
	if found == 0 {
		fmt.Println("none")
	}
}
