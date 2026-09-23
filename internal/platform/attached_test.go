package platform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAttachedHelper(t *testing.T) {
	switch os.Getenv("LAZYMLFLOW_ATTACHED_HELPER") {
	case "echo":
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		cwd, _ := os.Getwd()
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"stdin": line, "args": os.Args, "value": os.Getenv("CHILD_VALUE"), "cwd": cwd})
		fmt.Fprint(os.Stderr, "child stderr")
		os.Exit(7)
	case "wait":
		time.Sleep(time.Hour)
		os.Exit(0)
	}
}

func TestRunAttachedInDirPreservesWorkingDirectory(t *testing.T) {
	binary, _ := os.Executable()
	directory := t.TempDir()
	out := &bytes.Buffer{}
	code, err := RunAttachedInDir(context.Background(), []string{binary, "-test.run=^TestAttachedHelper$"}, append(os.Environ(), "LAZYMLFLOW_ATTACHED_HELPER=echo"), directory, strings.NewReader(""), out, &bytes.Buffer{})
	var result struct{ Cwd string }
	if err != nil || code != 7 || json.Unmarshal(out.Bytes(), &result) != nil {
		t.Fatalf("directory child: code=%d error=%v output=%s", code, err, out)
	}
	want, _ := filepath.EvalSymlinks(directory)
	got, _ := filepath.EvalSymlinks(result.Cwd)
	if got != want {
		t.Fatalf("child cwd=%q want %q", result.Cwd, directory)
	}
}

func TestRunAttachedStreamsArgumentsAndStatus(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "not-executed")
	args := []string{binary, "-test.run=^TestAttachedHelper$", "--", "literal space", "$(touch " + marker + ")", "; exit 42", "*"}
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := RunAttached(context.Background(), args, append(os.Environ(), "LAZYMLFLOW_ATTACHED_HELPER=echo", "CHILD_VALUE=selected"), strings.NewReader("typed line\n"), out, stderr)
	if err != nil || code != 7 || stderr.String() != "child stderr" {
		t.Fatalf("code %d error %v stderr %s", code, err, stderr)
	}
	var got struct {
		Stdin string   `json:"stdin"`
		Args  []string `json:"args"`
		Value string   `json:"value"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	if got.Stdin != "typed line\n" || got.Value != "selected" || !reflect.DeepEqual(got.Args, args) {
		t.Fatalf("child input: %+v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("argument evaluated as shell code")
	}
}

func TestRunAttachedCancellationAndMissingExecutable(t *testing.T) {
	binary, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := RunAttached(ctx, []string{binary, "-test.run=^TestAttachedHelper$"}, append(os.Environ(), "LAZYMLFLOW_ATTACHED_HELPER=wait"), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != context.DeadlineExceeded {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := RunAttached(context.Background(), []string{"definitely-no-lazymlflow-command"}, []string{"PATH="}, nil, nil, nil); err == nil {
		t.Fatal("missing executable succeeded")
	}
	if _, err := RunAttached(context.Background(), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("missing command succeeded")
	}
}

func TestAttachedExecutableUsesChildPATH(t *testing.T) {
	dir := t.TempDir()
	name := "custom-tool"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := attachedExecutable("custom-tool", []string{"PATH=" + dir})
	if err != nil || got != path {
		t.Fatalf("custom PATH: %q %v", got, err)
	}
}
