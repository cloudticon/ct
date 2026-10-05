//go:build !windows

package sync

import (
	"archive/tar"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Opening a FIFO blocks until a writer shows up, so a named pipe anywhere in
// the synced tree used to hang the initial sync forever.
func TestWriteTar_SkipsFIFOs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644))
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644))

	files, err := collectFiles(root, nil)
	require.NoError(t, err)

	done := make(chan error, 1)
	var buf bytes.Buffer
	go func() {
		_, err := writeTarFromFiles(&buf, root, files)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("writing the tar hung on a FIFO")
	}
	assert.Equal(t, []string{"a.txt"}, tarNames(t, buf.Bytes()))
}

// A unix socket in the tree (git fsmonitor, a local database socket, ...)
// used to abort the whole sync with "archive/tar: sockets not supported".
func TestWriteTar_SkipsSockets(t *testing.T) {
	root, err := os.MkdirTemp("", "ctsock")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644))
	l, err := net.Listen("unix", filepath.Join(root, "s.sock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	var buf bytes.Buffer
	_, err = writeTarFromRelativePaths(&buf, root, []string{"a.txt", "s.sock"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt"}, tarNames(t, buf.Bytes()))
}

// The tar header used the size from stat while the content was copied
// later; a file that grew in between (a log being appended to, an editor
// writing) failed the whole batch with "archive/tar: write too long".
// procfs files report size 0 but have content, which reproduces that
// deterministically.
func TestWriteTar_FileChangingSizeAfterStat(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("procfs not available")
	}
	root := t.TempDir()
	require.NoError(t, os.Symlink("/proc/self/status", filepath.Join(root, "growing.log")))

	var buf bytes.Buffer
	_, err := writeTarFromFiles(&buf, root, []string{"growing.log"})
	require.NoError(t, err)

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	h, err := tr.Next()
	require.NoError(t, err)
	assert.Equal(t, "growing.log", h.Name)
	content, err := io.ReadAll(tr)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Name:", "the entry must hold the file's actual content")
}

func tarNames(t *testing.T, data []byte) []string {
	t.Helper()
	return tarEntryNames(t, bytes.NewReader(data))
}

// One unreadable entry (a symlink loop, a root-owned 0600 file written by a
// container into a bind mount) used to abort the whole sync.
func TestWriteTar_SkipsUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644))
	require.NoError(t, os.Symlink("loop", filepath.Join(root, "loop")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.key"), []byte("k"), 0o000))

	var buf bytes.Buffer
	files, _, err := writeTar(&buf, root, []string{"loop", "secret.key", "a.txt"})
	require.NoError(t, err)
	assert.Equal(t, 1, files)
	assert.Equal(t, []string{"a.txt"}, tarNames(t, buf.Bytes()))
}

func TestWriteTar_PreservesModeAndLongNames(t *testing.T) {
	root := t.TempDir()
	long := filepath.Join("very", strings.Repeat("deeply-nested-directory-", 8), strings.Repeat("n", 120)+".sh")
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(long)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, long), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "żółw.txt"), []byte("utf8"), 0o640))

	var buf bytes.Buffer
	_, err := writeTarFromFiles(&buf, root, []string{filepath.ToSlash(long), "żółw.txt"})
	require.NoError(t, err)

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	modes := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		modes[h.Name] = h.Mode & 0o777
	}
	assert.Equal(t, int64(0o755), modes[filepath.ToSlash(long)], "long names survive and executables stay executable")
	assert.Equal(t, int64(0o640), modes["żółw.txt"])
}
