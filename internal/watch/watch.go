package watch

import (
	"fmt"

	"github.com/fsnotify/fsnotify"
)

// Event is a filesystem change event.
type Event struct {
	Path string
}

// Watcher manages filesystem watching across Linux (inotify) and macOS (FSEvents/kqueue).
type Watcher struct {
	fw      *fsnotify.Watcher
	watched map[string]bool
}

// New creates a new Watcher.
func New() (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}
	return &Watcher{fw: fw, watched: make(map[string]bool)}, nil
}

// Add starts watching path if not already watched.
func (w *Watcher) Add(path string) error {
	if w.watched[path] {
		return nil
	}
	if err := w.fw.Add(path); err != nil {
		return fmt.Errorf("watch %s: %w", path, err)
	}
	w.watched[path] = true
	return nil
}

// Events returns the channel on which write events are delivered.
// Only Write and Create events are forwarded (equivalent to IN_CLOSE_WRITE).
func (w *Watcher) Events() <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		for {
			select {
			case ev, ok := <-w.fw.Events:
				if !ok {
					close(ch)
					return
				}
				// Forward Write and Create events (closest to IN_CLOSE_WRITE)
				if ev.Has(fsnotify.Write) || ev.Has(fsnotify.Create) {
					ch <- Event{Path: ev.Name}
				}
			case err, ok := <-w.fw.Errors:
				if !ok {
					return
				}
				fmt.Printf("watch error: %v\n", err)
			}
		}
	}()
	return ch
}

// Close shuts down the watcher.
func (w *Watcher) Close() {
	w.fw.Close()
}
