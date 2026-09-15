//go:build linux

package socketactivation

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func socketFile(t *testing.T, network, name string) *os.File {
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
	fd, err := syscall.Dup(int(file.Fd()))
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	result := os.NewFile(uintptr(fd), name)
	t.Cleanup(func() { _ = result.Close() })
	return result
}

func TestInheritedSocketsServe(t *testing.T) {
	files := []*os.File{
		socketFile(t, "udp", "quic"),
		socketFile(t, "tcp", "https"),
		socketFile(t, "tcp", "ignored"),
		socketFile(t, "tcp", "http"),
	}
	sockets, err := fromFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	defer sockets.Close()
	for _, file := range files {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("original descriptor %q not closed: %v", file.Name(), err)
		}
	}
	if sockets.HTTPS == nil {
		t.Fatal("missing HTTPS listener")
	}
	// The duplicate remains usable after every original descriptor is closed.
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "activated")
	})}
	defer srv.Close()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(sockets.HTTP) }()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("http://" + sockets.HTTP.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "activated" {
		t.Fatalf("response: %q, %v", body, err)
	}
	_ = srv.Close()
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned %v", err)
	}
	conn, err := net.Dial("udp", sockets.QUIC.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("packet")); err != nil {
		t.Fatal(err)
	}
	_ = sockets.QUIC.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 32)
	n, _, err := sockets.QUIC.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != "packet" {
		t.Fatalf("UDP read: %q, %v", buffer[:n], err)
	}
}

func TestInvalidSocketsCloseDescriptors(t *testing.T) {
	for _, test := range []struct {
		name       string
		network    string
		socketName string
	}{
		{"duplicate", "tcp", "http"},
		{"udp-for-http", "udp", "http"},
		{"udp-for-https", "udp", "https"},
		{"tcp-for-quic", "tcp", "quic"},
	} {
		t.Run(test.name, func(t *testing.T) {
			firstName := "http"
			if test.name == "udp-for-http" {
				firstName = "https"
			}
			files := []*os.File{socketFile(t, "tcp", firstName), socketFile(t, test.network, test.socketName)}
			sockets, err := fromFiles(files)
			if err == nil {
				sockets.Close()
				t.Fatal("expected invalid activation sockets to fail")
			}
			for _, file := range files {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("original descriptor not closed: %v", err)
				}
			}
			listener := sockets.HTTP
			if listener == nil {
				listener = sockets.HTTPS
			}
			if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("converted listener not closed: %v", err)
			}
		})
	}
}

func TestReceiveIgnoresOtherProcess(t *testing.T) {
	t.Setenv("LISTEN_PID", "0")
	t.Setenv("LISTEN_FDS", "3")
	t.Setenv("LISTEN_FDNAMES", "http:https:quic")
	sockets, err := Receive()
	if err != nil || sockets.HTTP != nil || sockets.HTTPS != nil || sockets.QUIC != nil {
		t.Fatalf("unexpected activation: %+v, %v", sockets, err)
	}
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Fatalf("%s leaked to child environment", key)
		}
	}
}

func TestReceivePassedFDs(t *testing.T) {
	if os.Getenv("OPENLIST_ACTIVATION_TEST_CHILD") == "1" {
		// ExtraFiles provides fd 3 onwards, as systemd does. Only the child
		// knows its PID before exec, so set LISTEN_PID here.
		_ = os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
		sockets, err := Receive()
		if err != nil {
			t.Fatal(err)
		}
		defer sockets.Close()
		if sockets.HTTP == nil || sockets.HTTPS == nil || sockets.QUIC == nil {
			t.Fatalf("missing named socket: %+v", sockets)
		}
		second, err := Receive()
		if err != nil || second.HTTP != nil || second.HTTPS != nil || second.QUIC != nil {
			t.Fatalf("descriptors received twice: %+v, %v", second, err)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReceivePassedFDs$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "LISTEN_") && !strings.HasPrefix(entry, "OPENLIST_ACTIVATION_TEST_CHILD=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "OPENLIST_ACTIVATION_TEST_CHILD=1", "LISTEN_FDS=4", "LISTEN_FDNAMES=quic:ignored:https:http")
	cmd.ExtraFiles = []*os.File{
		socketFile(t, "udp", "quic"), socketFile(t, "tcp", "ignored"),
		socketFile(t, "tcp", "https"), socketFile(t, "tcp", "http"),
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("activation child: %v\n%s", err, output)
	}
}
