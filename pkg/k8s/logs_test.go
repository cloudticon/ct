package k8s

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type readCloserWithCloseFn struct {
	io.Reader
	closeFn func() error
}

func (r *readCloserWithCloseFn) Close() error {
	if r.closeFn != nil {
		return r.closeFn()
	}
	return nil
}

func TestStreamLogs_ReconnectsAndWritesPrefixedLines(t *testing.T) {
	origWait := waitForPodForLogsFn
	origStream := streamPodLogsForLogsFn
	origSleep := sleepForLogReconnectsFn
	t.Cleanup(func() {
		waitForPodForLogsFn = origWait
		streamPodLogsForLogsFn = origStream
		sleepForLogReconnectsFn = origSleep
	})

	sleepForLogReconnectsFn = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	waitForPodForLogsFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "pod-1", nil
	}

	streamCalls := 0
	streamPodLogsForLogsFn = func(_ context.Context, _ *client, _ string, _ *time.Time) (io.ReadCloser, error) {
		streamCalls++
		switch streamCalls {
		case 1:
			return nil, errors.New("temporary stream error")
		case 2:
			return &readCloserWithCloseFn{
				Reader: strings.NewReader("line-a\nline-b\n"),
				closeFn: func() error {
					cancel()
					return nil
				},
			}, nil
		default:
			return nil, context.Canceled
		}
	}

	var out bytes.Buffer
	err := streamLogs(ctx, &client{}, "remix", map[string]string{"app": "remix"}, &out)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, streamCalls, 2)
	assert.Contains(t, out.String(), "[remix]")
	assert.Contains(t, out.String(), "line-a")
	assert.Contains(t, out.String(), "line-b")
}

func TestStreamLogs_ReturnsWaitError(t *testing.T) {
	origWait := waitForPodForLogsFn
	t.Cleanup(func() {
		waitForPodForLogsFn = origWait
	})

	waitForPodForLogsFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "", errors.New("no pod found")
	}

	var out bytes.Buffer
	err := streamLogs(context.Background(), &client{}, "remix", map[string]string{"app": "remix"}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pod found")
}

func TestStreamLogs_ValidatesInput(t *testing.T) {
	err := streamLogs(context.Background(), nil, "remix", nil, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client is required")

	err = streamLogs(context.Background(), &client{}, "remix", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writer is required")
}

func TestLogPrefix_IsStableForTarget(t *testing.T) {
	prefixA := logPrefix("service-a")
	prefixB := logPrefix("service-a")
	assert.Equal(t, prefixA, prefixB)
	assert.Contains(t, prefixA, "[service-a]")
}

func stubLogs(t *testing.T) {
	t.Helper()
	origWait, origStream, origSleep, origDelay := waitForPodForLogsFn, streamPodLogsForLogsFn, sleepForLogReconnectsFn, logReconnectDelay
	t.Cleanup(func() {
		waitForPodForLogsFn, streamPodLogsForLogsFn, sleepForLogReconnectsFn, logReconnectDelay = origWait, origStream, origSleep, origDelay
	})
	waitForPodForLogsFn = func(_ context.Context, _ *client, _ map[string]string) (string, error) {
		return "pod-1", nil
	}
	sleepForLogReconnectsFn = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
}

// bufio.Scanner gives up on lines over 64 KiB ("token too long"): the stream
// was treated as interrupted, reconnected and replayed the whole log, hitting
// the same line again - forever.
func TestStreamLogs_HandlesVeryLongLines(t *testing.T) {
	stubLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	long := strings.Repeat("x", 200*1024)
	calls := 0
	streamPodLogsForLogsFn = func(_ context.Context, _ *client, _ string, _ *time.Time) (io.ReadCloser, error) {
		calls++
		if calls > 1 {
			cancel()
			return nil, context.Canceled
		}
		return io.NopCloser(strings.NewReader(long + "\nnext\n")), nil
	}

	var out bytes.Buffer
	require.NoError(t, streamLogs(ctx, &client{}, "web", map[string]string{"app": "web"}, &out))
	assert.Contains(t, out.String(), long)
	assert.Contains(t, out.String(), "[web] next\n")
}

// Reconnecting to the same pod (dropped connection, API server timeout)
// requested the full log again and printed every line a second time.
func TestStreamLogs_ReconnectToSamePodDoesNotReplay(t *testing.T) {
	stubLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sinceOnReconnect *time.Time
	calls := 0
	streamPodLogsForLogsFn = func(_ context.Context, _ *client, _ string, since *time.Time) (io.ReadCloser, error) {
		calls++
		switch calls {
		case 1:
			return io.NopCloser(io.MultiReader(
				strings.NewReader("2026-10-05T10:00:01.000000001Z first\n2026-10-05T10:00:02.000000002Z second\n"),
				iotest.ErrReader(errors.New("http2: stream reset")),
			)), nil
		case 2:
			sinceOnReconnect = since
			// The server resolves SinceTime to whole seconds and may resend
			// lines that were already printed.
			return &readCloserWithCloseFn{
				Reader: strings.NewReader("2026-10-05T10:00:02.000000002Z second\n2026-10-05T10:00:03.000000003Z third\r\n"),
				closeFn: func() error {
					cancel()
					return nil
				},
			}, nil
		default:
			return nil, context.Canceled
		}
	}

	var out bytes.Buffer
	require.NoError(t, streamLogs(ctx, &client{}, "web", map[string]string{"app": "web"}, &out))
	assert.Equal(t, "[web] first\n[web] second\n[web] third\n", stripANSI(out.String()))
	require.NotNil(t, sinceOnReconnect, "reconnect must ask only for newer lines")
	assert.Equal(t, "2026-10-05T10:00:02.000000002Z", sinceOnReconnect.Format(time.RFC3339Nano))
}

func TestStreamLogs_CancelDuringReconnectWaitReturnsPromptly(t *testing.T) {
	stubLogs(t)
	sleepForLogReconnectsFn = sleepContext
	logReconnectDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	streamPodLogsForLogsFn = func(_ context.Context, _ *client, _ string, _ *time.Time) (io.ReadCloser, error) {
		time.AfterFunc(20*time.Millisecond, cancel)
		return nil, errors.New("temporary")
	}

	done := make(chan error, 1)
	go func() { done <- streamLogs(ctx, &client{}, "web", nil, io.Discard) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("streamLogs did not return after cancel")
	}
}

func stripANSI(s string) string {
	return regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(s, "")
}
