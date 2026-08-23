package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounceDelay = 200 * time.Millisecond

// Watch observes root (a config directory or the directory holding a
// single config file) and calls onChange, debounced, when any watched
// JSON file changes. If only is not empty, events for other files are
// ignored. The returned stop function shuts the watcher down.
func Watch(root, only string, onChange func()) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := addTree(watcher, root); err != nil {
		watcher.Close()
		return nil, err
	}

	done := make(chan struct{})
	once := sync.Once{}

	go func() {
		var mu sync.Mutex
		var timer *time.Timer

		for {
			select {
			case <-done:
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
					continue
				}
				// Editors that replace files (atomic save) drop the watch
				// on the old inode; re-add the tree to recover.
				addTree(watcher, root)

				if only != "" && filepath.Base(event.Name) != filepath.Base(only) {
					continue
				}
				mu.Lock()
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(debounceDelay, onChange)
				mu.Unlock()

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				os.Stderr.WriteString("watcher error: " + err.Error() + "\n")
			}
		}
	}()

	stop = func() {
		once.Do(func() {
			close(done)
			watcher.Close()
		})
	}
	return stop, nil
}

// addTree adds root and every directory under it to the watcher.
func addTree(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep watching the rest
		}
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}
