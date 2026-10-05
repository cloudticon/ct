package k8s

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"time"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
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
	sleepForLogReconnectsFn = time.Sleep
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
	for {
		pod, err := waitForPodForLogsFn(ctx, c, selector)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		stream, err := streamPodLogsForLogsFn(ctx, c, pod)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("%s failed to open stream for %s (%s), reconnecting: %v", color.YellowString("[logs]"), targetName, pod, err)
			sleepForLogReconnectsFn(2 * time.Second)
			continue
		}

		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			fmt.Fprintf(w, "%s%s\n", prefix, scanner.Text())
		}
		_ = stream.Close()

		if scanErr := scanner.Err(); scanErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("%s stream interrupted for %s (%s), reconnecting: %v", color.YellowString("[logs]"), targetName, pod, scanErr)
			sleepForLogReconnectsFn(2 * time.Second)
			continue
		}

		if ctx.Err() != nil {
			return nil
		}
	}
}

func streamPodLogs(ctx context.Context, c *client, pod string) (io.ReadCloser, error) {
	if c.CoreV1 == nil {
		return nil, errors.New("kubernetes core/v1 client is required")
	}

	req := c.CoreV1.Pods(c.Namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: resolveContainer(ctx, c, pod, ""),
		Follow:    true,
	})
	stream, err := req.Stream(ctx)
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
