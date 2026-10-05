package k8s

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"strings"
	"time"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var logColorFns = []*color.Color{
	color.New(color.FgCyan),
	color.New(color.FgYellow),
	color.New(color.FgGreen),
	color.New(color.FgMagenta),
	color.New(color.FgBlue),
}

var (
	waitForPodForLogsFn     = waitForPod
	streamPodLogsForLogsFn  = streamPodLogs
	sleepForLogReconnectsFn = sleepContext
	logReconnectDelay       = 2 * time.Second
)

// streamLogs streams pod logs for a selected target and reconnects on pod/log stream churn.
func streamLogs(ctx context.Context, c *client, targetName string, selector map[string]string, w io.Writer) error {
	if c == nil {
		return errors.New("client is required")
	}
	if w == nil {
		return errors.New("writer is required")
	}

	prefix := logPrefix(targetName)
	var lastPod string
	var lastSeen time.Time // kubelet timestamp of the last line printed for lastPod
	for {
		pod, err := waitForPodForLogsFn(ctx, c, selector)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		// Reconnecting to the same pod (dropped connection, API server
		// timeout) must not print its whole log again: ask only for newer
		// lines and drop the ones already printed.
		var since *time.Time
		if pod == lastPod && !lastSeen.IsZero() {
			s := lastSeen
			since = &s
		} else {
			lastPod, lastSeen = pod, time.Time{}
		}

		stream, err := streamPodLogsForLogsFn(ctx, c, pod, since)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("%s failed to open stream for %s (%s), reconnecting: %v", color.YellowString("[logs]"), targetName, pod, err)
			if !sleepForLogReconnectsFn(ctx, logReconnectDelay) {
				return nil
			}
			continue
		}

		readErr := copyLogLines(stream, w, prefix, &lastSeen)
		_ = stream.Close()
		if ctx.Err() != nil {
			return nil
		}
		if readErr != nil {
			log.Printf("%s stream interrupted for %s (%s), reconnecting: %v", color.YellowString("[logs]"), targetName, pod, readErr)
		}
		// Also pause after a clean end of stream (container restarting, pod
		// going away) so a stream that ends right away cannot spin.
		if !sleepForLogReconnectsFn(ctx, logReconnectDelay) {
			return nil
		}
	}
}

// copyLogLines writes every line of r to w with prefix. Lines may be of any
// length (bufio.Scanner stops at 64 KiB). Lines carrying a kubelet timestamp
// have it stripped; lines not newer than *lastSeen are skipped (already
// printed before a reconnect) and *lastSeen tracks the newest line printed.
func copyLogLines(r io.Reader, w io.Writer, prefix string, lastSeen *time.Time) error {
	br := bufio.NewReader(r)
	printedUpTo := *lastSeen
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			writeLogLine(w, prefix, line, printedUpTo, lastSeen)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func writeLogLine(w io.Writer, prefix, line string, printedUpTo time.Time, lastSeen *time.Time) {
	text := strings.TrimRight(line, "\r\n")
	if ts, msg, ok := splitLogTimestamp(text); ok {
		if !printedUpTo.IsZero() && !ts.After(printedUpTo) {
			return // printed before the reconnect
		}
		*lastSeen = ts
		text = msg
	}
	fmt.Fprintf(w, "%s%s\n", prefix, text)
}

// splitLogTimestamp splits the RFC3339 timestamp the kubelet prepends to each
// line when PodLogOptions.Timestamps is set.
func splitLogTimestamp(line string) (time.Time, string, bool) {
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return time.Time{}, line, false
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:i])
	if err != nil {
		return time.Time{}, line, false
	}
	return ts, line[i+1:], true
}

func streamPodLogs(ctx context.Context, c *client, pod string, since *time.Time) (io.ReadCloser, error) {
	if c.CoreV1 == nil {
		return nil, errors.New("kubernetes core/v1 client is required")
	}

	opts := &corev1.PodLogOptions{
		Container:  resolveContainer(ctx, c, pod, ""),
		Follow:     true,
		Timestamps: true,
	}
	if since != nil {
		t := metav1.NewTime(*since)
		opts.SinceTime = &t
	}
	stream, err := c.CoreV1.Pods(c.Namespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening logs stream for pod %s: %w", pod, err)
	}
	return stream, nil
}

func logPrefix(targetName string) string {
	c := logColorFns[int(hashString(targetName))%len(logColorFns)]
	return c.Sprintf("[%s]", targetName) + " "
}

func hashString(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
