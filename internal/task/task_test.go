package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

// A poll that fails after the wait deadline with an error that doesn't wrap
// the context error (as the transport's flattened APIError) must still be
// recognisable as a timeout, while keeping the original error in the chain.
func TestWaitWrapsDeadlineOnPollError(t *testing.T) {
	apiErr := &protocol.APIError{Kind: protocol.KindTransport, Message: "context deadline exceeded (flattened)"}
	poll := func(ctx context.Context, _ protocol.TaskHandle) (protocol.TaskStatus, error) {
		<-ctx.Done()
		return protocol.TaskStatus{}, apiErr
	}
	_, err := Wait(context.Background(), poll, protocol.TaskHandle{UPID: "UPID:x"}, WaitOptions{Timeout: 50 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded in the chain, got %v", err)
	}
	var got *protocol.APIError
	if !errors.As(err, &got) || got != apiErr {
		t.Fatalf("original poll error lost from the chain: %v", err)
	}
}

// A poll error before any deadline is returned unchanged.
func TestWaitPollErrorUnchanged(t *testing.T) {
	want := errors.New("boom")
	poll := func(context.Context, protocol.TaskHandle) (protocol.TaskStatus, error) {
		return protocol.TaskStatus{}, want
	}
	_, err := Wait(context.Background(), poll, protocol.TaskHandle{}, WaitOptions{Timeout: time.Minute})
	if err != want {
		t.Fatalf("err = %v; want the poll error unchanged", err)
	}
}
