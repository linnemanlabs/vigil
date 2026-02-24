package main

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotifySystemd_NoSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")

	err := notifySystemd()
	if err == nil {
		t.Fatal("expected error when NOTIFY_SOCKET is empty")
	}
	if !strings.Contains(err.Error(), "NOTIFY_SOCKET not set") {
		t.Errorf("error = %q, want substring %q", err, "NOTIFY_SOCKET not set")
	}
}

func TestNotifySystemd_InvalidPath(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "nonexistent.sock"))

	err := notifySystemd()
	if err == nil {
		t.Fatal("expected error for nonexistent socket")
	}
	if !strings.Contains(err.Error(), "dial failed") {
		t.Errorf("error = %q, want substring %q", err, "dial failed")
	}
}

func TestNotifySystemd_Success(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "notify.sock")

	// Create a real unixgram listener.
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(context.Background(), "unixgram", sockPath)
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	defer func() { _ = conn.Close() }()

	t.Setenv("NOTIFY_SOCKET", sockPath)

	if err := notifySystemd(); err != nil {
		t.Fatalf("notifySystemd() = %v, want nil", err)
	}

	buf := make([]byte, 256)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read from socket: %v", err)
	}

	got := string(buf[:n])
	if got != "READY=1" {
		t.Errorf("payload = %q, want %q", got, "READY=1")
	}
}
