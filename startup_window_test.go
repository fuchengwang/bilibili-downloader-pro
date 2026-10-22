package main

import (
	"context"
	"testing"
)

func TestWakeupDoesNotShowWindowBeforeInitialPageIsReady(t *testing.T) {
	// No Wails frontend is installed in this context: an early wakeup must return
	// without attempting any native window or frontend event operation.
	app := &App{ctx: context.Background()}
	app.ShowMainWindow()
}
