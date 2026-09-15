//go:build linux

package cmd

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
)

func TestServerRejectsInvalidActivation(t *testing.T) {
	if mode := os.Getenv("OPENLIST_ACTIVATION_TEST_MODE"); mode != "" {
		// ExtraFiles supplies fd 3 onwards; the child must supply its own PID.
		if err := os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid())); err != nil {
			t.Fatal(err)
		}
		dataDir := os.Getenv("OPENLIST_ACTIVATION_TEST_DATA")
		if mode == "embedded" {
			flags.DataDir = dataDir
			flags.LogStd = true
			err := OutOpenListInit()
			if err == nil || !strings.Contains(err.Error(), "failed to receive activation sockets:") {
				t.Fatalf("expected activation error, got %v", err)
			}
			t.Log(err)
			return
		}
		RootCmd.SetArgs([]string{"server", "--data", dataDir, "--log-std"})
		Execute()
		t.Fatal("server command returned without exiting on activation failure")
	}

	for _, mode := range []string{"cli", "embedded"} {
		for _, test := range []struct {
			name     string
			networks []string
			names    string
			want     string
		}{
			{"duplicate", []string{"tcp", "tcp"}, "http:http", `duplicate activation socket "http"`},
			{"wrong-type", []string{"udp"}, "http", `activation socket "http"`},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerRejectsInvalidActivation$", "-test.v")
				for _, entry := range os.Environ() {
					if !strings.HasPrefix(entry, "LISTEN_") && !strings.HasPrefix(entry, "OPENLIST_") {
						child.Env = append(child.Env, entry)
					}
				}
				child.Env = append(child.Env,
					"OPENLIST_ACTIVATION_TEST_MODE="+mode,
					"OPENLIST_ACTIVATION_TEST_DATA="+t.TempDir(),
					"LISTEN_FDS="+strconv.Itoa(len(test.networks)),
					"LISTEN_FDNAMES="+test.names,
				)
				for _, network := range test.networks {
					child.ExtraFiles = append(child.ExtraFiles, activationTestFile(t, network))
				}
				output, err := child.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("server did not exit after activation failure: %v\n%s", ctx.Err(), output)
				}
				if mode == "cli" {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
						t.Fatalf("expected exit status 1, got %v\n%s", err, output)
					}
				} else if err != nil {
					t.Fatalf("embedded startup did not return its error: %v\n%s", err, output)
				}
				if !strings.Contains(string(output), "failed to receive activation sockets: "+test.want) {
					t.Fatalf("missing activation error in output:\n%s", output)
				}
			})
		}
	}
}

func activationTestFile(t *testing.T, network string) *os.File {
	t.Helper()
	var file *os.File
	var err error
	if network == "tcp" {
		listener, e := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if e != nil {
			t.Fatal(e)
		}
		file, err = listener.File()
		_ = listener.Close()
	} else {
		conn, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if e != nil {
			t.Fatal(e)
		}
		file, err = conn.File()
		_ = conn.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
