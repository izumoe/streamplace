//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"stream.place/streamplace/pkg/renditions"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "-listen" || os.Args[1] == "-job") {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSIGTERMReapsActiveJob(t *testing.T) {
	// Adopt any orphan on failure so the regression itself cannot leak a job.
	require.NoError(t, unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0))
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	logs, err := os.Create(filepath.Join(t.TempDir(), "service.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = logs.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	service := exec.CommandContext(ctx, os.Args[0], "-listen", address, "-jobs", "1")
	service.Stdout, service.Stderr = logs, logs
	require.NoError(t, service.Start())
	childPID := 0
	t.Cleanup(func() {
		if service.ProcessState == nil {
			_ = service.Process.Kill()
			_ = service.Wait()
		}
		if childPID != 0 {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
			_, _ = syscall.Wait4(childPID, nil, 0, nil)
		}
	})
	client := &http.Client{Timeout: 15 * time.Second}
	require.Eventually(t, func() bool {
		response, err := client.Get("http://" + address + "/")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusNotFound
	}, 5*time.Second, 10*time.Millisecond)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormField("profiles")
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(part).Encode([]renditions.Profile{
		{Name: "large", Width: 1920, Height: 1080, Bitrate: 6_000_000, Profile: "high"},
	}))
	part, err = writer.CreateFormFile("source", "segment.mp4")
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "5sec.mp4"))
	require.NoError(t, err)
	_, err = part.Write(source)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := client.Post("http://"+address+"/transcode", writer.FormDataContentType(), &body)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	require.Eventually(t, func() bool {
		paths, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", service.Process.Pid))
		if err != nil {
			return false
		}
		for _, path := range paths {
			children, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, pid := range strings.Fields(string(children)) {
				command, err := os.ReadFile("/proc/" + pid + "/cmdline")
				if err == nil && bytes.Contains(command, []byte("\x00-job\x00")) {
					childPID, err = strconv.Atoi(pid)
					return err == nil
				}
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
	// Hold a real native child active even on machines that encode this fixture quickly.
	require.NoError(t, syscall.Kill(childPID, syscall.SIGSTOP))
	select {
	case <-requestDone:
		t.Fatal("transcode completed before SIGTERM")
	default:
	}
	require.NoError(t, service.Process.Signal(syscall.SIGTERM))
	err = service.Wait()
	diagnostics, readErr := os.ReadFile(logs.Name())
	require.NoError(t, readErr)
	require.NoError(t, err, string(diagnostics))
	require.ErrorIs(t, syscall.Kill(childPID, 0), syscall.ESRCH, "service exited before reaping its active native child")
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("active HTTP request did not finish after shutdown")
	}
}
