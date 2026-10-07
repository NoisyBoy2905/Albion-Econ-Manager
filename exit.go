package main

import (
	"os"
	"os/signal"
	"syscall"
)

// flushOnExit saves the price book when the program is asked to stop (Ctrl+C,
// or the console window closing), so the last prices seen aren't lost now that
// saves are debounced.
func flushOnExit(app *App) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		app.book.Flush()
		app.hist.Close()
		os.Exit(0)
	}()
}
