//go:build !darwin

package main

import (
	"context"
	"errors"
	"flag"
	"time"
)

var testDecide = flag.String("test-decide", "", "answer every ask allow or deny after 1s instead of showing a dialog")

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
