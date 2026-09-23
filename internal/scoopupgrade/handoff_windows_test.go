//go:build windows

package scoopupgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeHelperWaitsForExactParentAndCancelsBeforeManager(t *testing.T) {
	exe, product, options := fixture(t)
	plan, err := Prepare(context.Background(), exe, product, options)
	if err != nil {
		t.Fatal(err)
	}
	r := *plan.request
	r.ParentPID, r.ParentStarted = os.Getpid(), processStarted(os.Getpid())
	r.OperationID = "0123456789abcdef0123456789abcdef"
	dir := filepath.Join(t.TempDir(), r.OperationID)
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = secureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- runHelperContext(ctx, r, dir) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, e := readStatus(filepath.Dir(dir), r.OperationID)
		if e == nil && state.Status == "waiting" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not reach parent wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case code := <-done:
		t.Fatalf("helper ran while parent was alive: %d", code)
	case <-time.After(80 * time.Millisecond):
	}
	cancel()
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("cancellation reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop helper")
	}
	state, err := readStatus(filepath.Dir(dir), r.OperationID)
	if err != nil || state.Status != "canceled" || state.ChangeKnown || !strings.Contains(state.Reason, "before starting Scoop") {
		t.Fatalf("wrong cancellation: %+v %v", state, err)
	}
	body, err := os.ReadFile(exe)
	if err != nil || string(body) != "fixture-product" {
		t.Fatal("canceled handoff changed executable")
	}
}
