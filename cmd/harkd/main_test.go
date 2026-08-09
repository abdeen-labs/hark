package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// TestServeDrainsInFlightRequests checks the graceful path: a request already
// being handled when the signal arrives must complete.
func TestServeDrainsInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
	})}
	ln := listen(t)
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, discardLogger(), srv, ln, 5*time.Second) }()

	body := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			body <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}
	cancel() // the SIGTERM equivalent, mid-request

	select {
	case got := <-body:
		if got != "done" {
			t.Errorf("in-flight request returned %q, want %q", got, "done")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after shutdown")
	}
}

// TestServeGivesUpAfterGrace checks the ungraceful path: a handler that outlives
// the grace period must not wedge the process.
func TestServeGivesUpAfterGrace(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	srv := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	})}
	ln := listen(t)
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, discardLogger(), srv, ln, 100*time.Millisecond) }()

	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			resp.Body.Close()
		}
	}()

	time.Sleep(200 * time.Millisecond) // let the request reach the handler
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "graceful shutdown") {
			t.Errorf("serve returned %v, want a graceful-shutdown timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve hung past the grace period")
	}
}

// TestServeReportsListenerFailure covers Serve returning on its own.
func TestServeReportsListenerFailure(t *testing.T) {
	ln := listen(t)
	srv := &http.Server{Handler: http.NotFoundHandler()}

	done := make(chan error, 1)
	go func() { done <- serve(t.Context(), discardLogger(), srv, ln, time.Second) }()

	time.Sleep(50 * time.Millisecond)
	_ = ln.Close() // Serve now fails with "use of closed network connection"

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "serve") {
			t.Errorf("serve returned %v, want a serve error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not report the listener failure")
	}
}

func TestBuildVersionIsNeverEmpty(t *testing.T) {
	if got := buildVersion(); got == "" {
		t.Error("buildVersion() = \"\", want a non-empty identifier")
	}
}

func TestShortRevision(t *testing.T) {
	for in, want := range map[string]string{
		"a1b2c3d4e5f6a7b8c9d0": "a1b2c3d4e5f6",
		"a1b2c3":               "a1b2c3",
		"":                     "",
	} {
		if got := shortRevision(in); got != want {
			t.Errorf("shortRevision(%q) = %q, want %q", in, got, want)
		}
	}
}
