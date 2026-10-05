//go:build !windows

package sync

import (
	"archive/tar"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
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
