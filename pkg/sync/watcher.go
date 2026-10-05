package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
)

const (
	debounceWindow = 300 * time.Millisecond
	// maxDebounce bounds how long a continuous stream of events (e.g. a
	// build writing files) can postpone a flush.
	maxDebounce = 2 * time.Second
)

// newFSWatcher is a seam for tests.
var newFSWatcher = fsnotify.NewWatcher

// ChangeType describes a file system change type.
type ChangeType int

const (
	ChangeCreate ChangeType = iota
	ChangeModify
	ChangeDelete
)

// FileChange is a single file change relative to Watcher.root.
type FileChange struct {
	Path string
	Type ChangeType
}

// Watcher emits batched file changes for a directory.
type Watcher struct {
	root      string
	exclude   []string
	polling   bool
	excluder  *pathExcluder
	pollEvery time.Duration

	mu  stdsync.Mutex
	err error
}

// NewWatcher creates a watcher for root.
func NewWatcher(root string, exclude []string, polling bool) (*Watcher, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("watch root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("watch root must be a directory")
	}

	excluder, err := newPathExcluder(exclude)
	if err != nil {
		return nil, err
	}

	return &Watcher{
		root:      absRoot,
		exclude:   append([]string(nil), exclude...),
		polling:   polling,
		excluder:  excluder,
		pollEvery: 500 * time.Millisecond,
	}, nil
}

// Watch starts watching and returns a channel of debounced change batches.
// It returns only once the watcher is established, so every change made
// after Watch returns is reported. The channel is closed when ctx is done or
// the watcher fails; Err reports the failure.
func (w *Watcher) Watch(ctx context.Context) (<-chan []FileChange, error) {
	out := make(chan []FileChange)
	if w.polling {
		prev, err := w.snapshot()
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", w.root, err)
		}
		go w.pollLoop(ctx, prev, out)
		return out, nil
	}

	fw, err := newFSWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating file watcher: %w%s", err, watchLimitHint(err))
	}
	if err := w.addWatches(fw, w.root, nil); err != nil {
		_ = fw.Close()
		return nil, err
	}
	go w.fsnotifyLoop(ctx, fw, out)
	return out, nil
}

// Err returns the error that stopped the watcher, if any.
func (w *Watcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *Watcher) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
}

// addWatches watches dir and every non-excluded directory below it. When
// onFile is set it is called for every non-excluded file found on the way.
// Running out of watches is an error: silently unwatched directories would
// mean silently unsynced changes.
func (w *Watcher) addWatches(fw *fsnotify.Watcher, dir string, onFile func(path string)) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path != w.root && w.isExcluded(path, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			if onFile != nil {
				onFile(path)
			}
			return nil
		}
		if err := fw.Add(path); err != nil && isWatchLimitError(err) {
			return fmt.Errorf("watching %s: %w%s", path, err, watchLimitHint(err))
		}
		return nil
	})
}

func isWatchLimitError(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

func watchLimitHint(err error) string {
	if !isWatchLimitError(err) {
		return ""
	}
	return " (the OS file-watch limit is exhausted: exclude large directories such as node_modules, " +
		"raise fs.inotify.max_user_watches / fs.inotify.max_user_instances on Linux, " +
		"or set polling: true on this sync rule)"
}

func (w *Watcher) fsnotifyLoop(ctx context.Context, fw *fsnotify.Watcher, out chan<- []FileChange) {
	defer close(out)
	defer fw.Close()

	pending := make(map[string]FileChange)
	var pendingSince time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	flushDue := false

	add := func(change FileChange) {
		if len(pending) == 0 {
			pendingSince = time.Now()
		}
		if prev, exists := pending[change.Path]; exists {
			pending[change.Path] = mergeChangeTypes(prev, change)
		} else {
			pending[change.Path] = change
		}
		// Debounce, but never postpone a flush beyond maxDebounce.
		if !flushDue && time.Since(pendingSince) < maxDebounce {
			timer.Reset(debounceWindow)
		}
	}
	addFile := func(path string, typ ChangeType) {
		if rel, err := w.rel(path); err == nil && rel != "." {
			add(FileChange{Path: rel, Type: typ})
		}
	}

	for {
		// Keep consuming events while the receiver is busy (initial sync,
		// slow upload): changes are merged into pending instead of piling
		// up in the kernel queue, which can overflow and drop events.
		var send chan<- []FileChange
		var batch []FileChange
		if flushDue && len(pending) > 0 {
			send = out
			batch = make([]FileChange, 0, len(pending))
			for _, change := range pending {
				batch = append(batch, change)
			}
		}

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case send <- batch:
			pending = make(map[string]FileChange)
			flushDue = false
		case <-timer.C:
			flushDue = true
		case err, ok := <-fw.Errors:
			if !ok {
				w.setErr(errors.New("file watcher stopped"))
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				// Events were dropped: rescan everything so nothing is missed.
				if err := w.addWatches(fw, w.root, func(path string) { addFile(path, ChangeModify) }); err != nil {
					w.setErr(err)
					return
				}
			}
		case event, ok := <-fw.Events:
			if !ok {
				w.setErr(errors.New("file watcher stopped"))
				return
			}

			isDir := pathExistsAndIsDir(event.Name)
			if w.isExcluded(event.Name, isDir) {
				continue
			}

			if event.Op&fsnotify.Create != 0 && isDir {
				if err := w.addWatches(fw, event.Name, nil); err != nil {
					w.setErr(err)
					return
				}
			}

			change, ok := w.toFileChange(event)
			if !ok {
				continue
			}
			add(change)
		}
	}
}

func (w *Watcher) pollLoop(ctx context.Context, prev map[string]fileState, out chan<- []FileChange) {
	defer close(out)

	ticker := time.NewTicker(w.pollEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			next, err := w.snapshot()
			if err != nil {
				continue
			}
			changes := diffSnapshots(prev, next)
			if len(changes) > 0 {
				select {
				case out <- changes:
				case <-ctx.Done():
					return
				}
			}
			prev = next
		}
	}
}

func (w *Watcher) snapshot() (map[string]fileState, error) {
	result := make(map[string]fileState)
	err := filepath.WalkDir(w.root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		isDir := d.IsDir()
		if w.isExcluded(path, isDir) && path != w.root {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		if isDir {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := w.rel(path)
		if err != nil {
			return nil
		}
		result[rel] = fileState{
			Size:    info.Size(),
			ModTime: info.ModTime().UnixNano(),
		}
		return nil
	})
	return result, err
}

type fileState struct {
	Size    int64
	ModTime int64
}

func diffSnapshots(prev, next map[string]fileState) []FileChange {
	changes := make([]FileChange, 0)
	for path, before := range prev {
		after, exists := next[path]
		if !exists {
			changes = append(changes, FileChange{Path: path, Type: ChangeDelete})
			continue
		}
		if before != after {
			changes = append(changes, FileChange{Path: path, Type: ChangeModify})
		}
	}
	for path := range next {
		if _, exists := prev[path]; !exists {
			changes = append(changes, FileChange{Path: path, Type: ChangeCreate})
		}
	}
	return changes
}

func (w *Watcher) toFileChange(event fsnotify.Event) (FileChange, bool) {
	rel, err := w.rel(event.Name)
	if err != nil || rel == "" || rel == "." {
		return FileChange{}, false
	}

	switch {
	case event.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
		return FileChange{Path: rel, Type: ChangeDelete}, true
	case event.Op&fsnotify.Create != 0:
		return FileChange{Path: rel, Type: ChangeCreate}, true
	case event.Op&(fsnotify.Write|fsnotify.Chmod) != 0:
		return FileChange{Path: rel, Type: ChangeModify}, true
	default:
		return FileChange{}, false
	}
}

func mergeChangeTypes(prev, next FileChange) FileChange {
	if next.Type == ChangeDelete {
		return next
	}
	if prev.Type == ChangeCreate && next.Type == ChangeModify {
		return prev
	}
	return next
}

func (w *Watcher) rel(path string) (string, error) {
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func pathExistsAndIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (w *Watcher) isExcluded(path string, isDir bool) bool {
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return false
	}
	return w.excluder.IsExcluded(rel, isDir)
}

type pathExcluder struct {
	rules []excludeRule
}

type excludeRule struct {
	pattern  string
	negated  bool
	anchored bool
	dirOnly  bool
}

func newPathExcluder(patterns []string) (*pathExcluder, error) {
	rules := make([]excludeRule, 0, len(patterns))
	for _, raw := range patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "#") {
			continue
		}

		rule := excludeRule{}
		if strings.HasPrefix(raw, "!") {
			rule.negated = true
			raw = strings.TrimPrefix(raw, "!")
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "/") {
			rule.anchored = true
			raw = strings.TrimPrefix(raw, "/")
		}
		if strings.HasSuffix(raw, "/") {
			rule.dirOnly = true
			raw = strings.TrimSuffix(raw, "/")
		}
		rule.pattern = filepath.ToSlash(raw)

		rules = append(rules, rule)
	}
	return &pathExcluder{rules: rules}, nil
}

func (e *pathExcluder) IsExcluded(rel string, isDir bool) bool {
	excluded := false
	for _, rule := range e.rules {
		if rule.matches(rel, isDir) {
			excluded = !rule.negated
		}
	}
	return excluded
}

func (r excludeRule) matches(rel string, isDir bool) bool {
	rel = filepath.ToSlash(rel)
	if r.dirOnly && !isDir {
		// For dir-only rules support descendants too.
		return strings.HasPrefix(rel+"/", r.pattern+"/")
	}
	if r.dirOnly && isDir && strings.HasPrefix(rel+"/", r.pattern+"/") {
		return true
	}

	if r.anchored {
		if !strings.ContainsAny(r.pattern, "*?[") {
			return rel == r.pattern || strings.HasPrefix(rel, r.pattern+"/")
		}
		return matchPattern(r.pattern, rel)
	}

	if !strings.Contains(r.pattern, "/") {
		if matchPattern(r.pattern, filepath.Base(rel)) {
			return true
		}
		return matchPattern("**/"+r.pattern, rel)
	}

	if matchPattern(r.pattern, rel) {
		return true
	}
	return matchPattern("**/"+r.pattern, rel)
}

func matchPattern(pattern, rel string) bool {
	ok, err := doublestar.PathMatch(pattern, rel)
	return err == nil && ok
}
