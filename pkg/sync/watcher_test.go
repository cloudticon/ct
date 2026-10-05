package sync

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWatcher_ValidatesRoot(t *testing.T) {
	_, err := NewWatcher("", nil, false)
	require.Error(t, err)

	filePath := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))
	_, err = NewWatcher(filePath, nil, false)
	require.Error(t, err)
}

func TestPathExcluder_GitignoreLikeRules(t *testing.T) {
	excluder, err := newPathExcluder([]string{
		"/node_modules",
		"*.log",
		"**/*.tmp",
		"!important.log",
	})
	require.NoError(t, err)

	assert.True(t, excluder.IsExcluded("node_modules/pkg/index.js", false))
	assert.True(t, excluder.IsExcluded("logs/app.log", false))
	assert.True(t, excluder.IsExcluded("a/b/file.tmp", false))
	assert.False(t, excluder.IsExcluded("important.log", false))
	assert.False(t, excluder.IsExcluded("src/main.ts", false))
}

func TestWatcherPolling_EmitsCreateAndDelete(t *testing.T) {
	root := t.TempDir()

	w, err := NewWatcher(root, []string{"*.tmp"}, true)
	require.NoError(t, err)
	w.pollEvery = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := w.Watch(ctx)
	require.NoError(t, err)

	target := filepath.Join(root, "notes.txt")
	require.NoError(t, os.WriteFile(target, []byte("hello"), 0o644))

	batch := waitBatch(t, ch)
	assert.Contains(t, batch, FileChange{Path: "notes.txt", Type: ChangeCreate})

	require.NoError(t, os.Remove(target))
	batch = waitBatch(t, ch)
	assert.Contains(t, batch, FileChange{Path: "notes.txt", Type: ChangeDelete})
}

func TestWatcherFsnotify_DebouncesRapidChanges(t *testing.T) {
	root := t.TempDir()
	w, err := NewWatcher(root, nil, false)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := w.Watch(ctx)
	require.NoError(t, err)
	target := filepath.Join(root, "rapid.txt")

	require.NoError(t, os.WriteFile(target, []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(target, []byte("b"), 0o644))
	require.NoError(t, os.WriteFile(target, []byte("c"), 0o644))

	batch := waitBatch(t, ch)

	count := 0
	for _, c := range batch {
		if c.Path == "rapid.txt" {
			count++
		}
	}
	assert.Equal(t, 1, count, "expected debounced single change for rapid writes")
}

func waitBatch(t *testing.T, ch <-chan []FileChange) []FileChange {
	t.Helper()
	select {
	case batch := <-ch:
		return batch
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watcher batch")
		return nil
	}
}

func TestWatcher_ReportsWatcherCreationFailureWithHint(t *testing.T) {
	orig := newFSWatcher
	t.Cleanup(func() { newFSWatcher = orig })
	newFSWatcher = func() (*fsnotify.Watcher, error) { return nil, syscall.EMFILE }

	w, err := NewWatcher(t.TempDir(), nil, false)
	require.NoError(t, err)
	_, err = w.Watch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "polling: true", "the error must tell the user how to get unstuck")
}

// A watcher that could not start used to close its channel silently, so
// Syncer.Run returned nil and sync stopped without any message.
func TestSyncerRun_FailsWhenWatcherCannotStart(t *testing.T) {
	orig := newFSWatcher
	t.Cleanup(func() { newFSWatcher = orig })
	newFSWatcher = func() (*fsnotify.Watcher, error) { return nil, syscall.EMFILE }

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644))
	s := NewSyncer(&fakePodExecutor{}, "demo", map[string]string{"app": "x"}, SyncRule{From: root, To: "/app"})

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too many open files")
	case <-time.After(2 * time.Second):
		t.Fatal("Run kept running although no file watcher could be created")
	}
}

// Moving a populated directory into the tree (mv, git checkout, unzip, cp -r)
// yields one Create event for the directory and none for the files inside.
// The files used to be skipped (directories are not tarred) and never synced.
func TestWatcherFsnotify_ReportsFilesInsideNewDirectory(t *testing.T) {
	root := t.TempDir()
	w, err := NewWatcher(root, []string{"*.tmp"}, false)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := w.Watch(ctx)
	require.NoError(t, err)

	staging := filepath.Join(t.TempDir(), "pkg")
	require.NoError(t, os.MkdirAll(filepath.Join(staging, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "a.txt"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "sub", "b.txt"), []byte("b"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "skip.tmp"), []byte("x"), 0o644))
	require.NoError(t, os.Rename(staging, filepath.Join(root, "pkg")))

	seen := map[string]ChangeType{}
	reported := func(path string) bool {
		typ, ok := seen[path]
		return ok && typ == ChangeCreate
	}
	deadline := time.After(3 * time.Second)
	for !reported("pkg/a.txt") || !reported("pkg/sub/b.txt") {
		select {
		case batch := <-ch:
			for _, c := range batch {
				seen[c.Path] = c.Type
			}
		case <-deadline:
			t.Fatalf("files inside the new directory were not reported; got %v", seen)
		}
	}
	_, excluded := seen["pkg/skip.tmp"]
	assert.False(t, excluded, "excluded files must not be reported")
}

func TestPathExcluder_DirOnlyRulesAndDescendants(t *testing.T) {
	excluder, err := newPathExcluder([]string{"build/", "node_modules", "/dist"})
	require.NoError(t, err)

	assert.True(t, excluder.IsExcluded("build", true))
	assert.True(t, excluder.IsExcluded("src/build", true), "dir-only rules without a slash match at any depth")
	assert.True(t, excluder.IsExcluded("src/build/out.js", false), "everything below an excluded directory is excluded")
	assert.True(t, excluder.IsExcluded("packages/a/node_modules/x/index.js", false))
	assert.True(t, excluder.IsExcluded("dist/app.js", false))
	assert.False(t, excluder.IsExcluded("src/dist/app.js", false), "anchored rules only match at the root")
	assert.False(t, excluder.IsExcluded("src/builder.go", false))
}

// Deleting an excluded directory locally must not delete it in the
// container: it was excluded precisely so the container keeps its own copy
// (build output, dependencies, ...).
func TestWatcherFsnotify_IgnoresDeletionOfExcludedDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src", "build"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "build", "out.js"), []byte("x"), 0o644))

	w, err := NewWatcher(root, []string{"build/"}, false)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := w.Watch(ctx)
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(filepath.Join(root, "src", "build")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "marker.txt"), []byte("m"), 0o644))

	var seen []FileChange
	for {
		batch := waitBatch(t, ch)
		seen = append(seen, batch...)
		if containsPath(seen, "src/marker.txt") {
			break
		}
	}
	assert.False(t, containsPath(seen, "src/build"), "deleting an excluded directory must not be synced: %v", seen)
}

func containsPath(changes []FileChange, path string) bool {
	for _, c := range changes {
		if c.Path == path {
			return true
		}
	}
	return false
}
