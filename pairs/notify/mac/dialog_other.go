//go:build !darwin

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

var testDecide = flag.String("test-decide", "", "answer every dialog allow or deny after 1s instead of showing it")

func checkFlags() error {
	if *testDecide != "allow" && *testDecide != "deny" {
		return errors.New("dialogs need macOS; elsewhere pass --test-decide allow|deny")
	}
	return nil
}

func decide(ctx context.Context, _, _ string) bool {
	select {
	case <-time.After(time.Second):
		return *testDecide == "allow"
	case <-ctx.Done():
		return false
	}
}

func showNotification(_ context.Context, title, text string) error {
	_, err := fmt.Fprintf(os.Stderr, "notification: %s: %s\n", title, text)
	return err
}
