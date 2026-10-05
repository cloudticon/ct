package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortRule describes a single local->remote forwarding rule.
type PortRule struct {
	Local  int
	Remote int
}

var (
	waitForPodFn   = waitForPod
	forwardPortsFn = forwardPorts

	// portForwardBackoff is the wait before the first reconnect; it doubles
	// up to maxPortForwardBackoff and resets once a forward stayed up for
	// portForwardStableAfter.
	portForwardBackoff     = time.Second
	maxPortForwardBackoff  = 10 * time.Second
	portForwardStableAfter = 10 * time.Second
)

// portForward starts port forwarding for the selected workload and reconnects on connection loss.
func portForward(ctx context.Context, c *client, selector map[string]string, ports []PortRule) error {
	if c == nil {
		return errors.New("client is required")
	}
	if len(ports) == 0 {
		return errors.New("at least one port rule is required")
	}

	backoff := portForwardBackoff
	for {
		pod, err := waitForPodFn(ctx, c, selector)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		started := time.Now()
		err = forwardPortsFn(ctx, c, pod, ports)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if isLocalListenError(err) {
			return fmt.Errorf("port-forward to pod %s: could not listen on local port(s) %s - already in use by another process (or another ct dev session)? %w",
				pod, localPorts(ports), err)
		}
		if isAccessDenied(err) {
			return fmt.Errorf("port-forward to pod %s denied (port-forwarding needs create on pods/portforward): %w", pod, err)
		}
		if time.Since(started) >= portForwardStableAfter {
			backoff = portForwardBackoff
		}
		log.Printf("%s connection lost for pod %s, reconnecting in %s: %v", color.YellowString("[port-forward]"), pod, backoff, err)
		if !sleepContext(ctx, backoff) {
			return nil
		}
		backoff *= 2
		if backoff > maxPortForwardBackoff {
			backoff = maxPortForwardBackoff
		}
	}
}

// isLocalListenError reports client-go's "no local port could be opened"
// failure, which reconnecting cannot fix.
func isLocalListenError(err error) bool {
	return strings.Contains(err.Error(), "unable to listen on any of the requested ports")
}

// isAccessDenied reports an RBAC/authentication refusal of the upgrade.
// client-go flattens the API status into the message, so match on it.
func isAccessDenied(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "is forbidden") || strings.Contains(msg, "Unauthorized")
}

func localPorts(ports []PortRule) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p.Local))
	}
	return strings.Join(parts, ", ")
}

// sleepContext waits for d and reports whether it did so before ctx ended.
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func forwardPorts(ctx context.Context, c *client, pod string, ports []PortRule) error {
	if c.Config == nil {
		return errors.New("rest config is required for port-forwarding")
	}
	if c.CoreV1 == nil {
		return errors.New("kubernetes core/v1 client is required for port-forwarding")
	}

	reqURL := c.CoreV1.RESTClient().
		Post().
		Resource("pods").
		Namespace(c.Namespace).
		Name(pod).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(c.Config)
	if err != nil {
		return fmt.Errorf("creating spdy round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, reqURL)
	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	// done ends the helper goroutines below when ForwardPorts returns,
	// whatever the reason; they used to leak on every failed attempt.
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			close(stopCh)
		case <-done:
		}
	}()

	// client-go reports ports it could not listen on to errOut; a partially
	// working forward must not fail silently.
	errOut := &lineLogger{prefix: color.YellowString("[port-forward]")}
	forwarder, err := portforward.New(dialer, toPFPorts(ports), stopCh, readyCh, io.Discard, errOut)
	if err != nil {
		return fmt.Errorf("creating port forwarder: %w", err)
	}

	go func() {
		select {
		case <-readyCh:
		case <-done:
			return
		}
		for _, p := range ports {
			log.Printf("%s localhost:%d -> %s:%d", color.CyanString("[port-forward]"), p.Local, pod, p.Remote)
		}
	}()

	if err := forwarder.ForwardPorts(); err != nil {
		return fmt.Errorf("forwarding ports for pod %s: %w", pod, err)
	}

	return nil
}

// lineLogger forwards every line written to it to the standard logger.
type lineLogger struct {
	prefix string
}

func (l *lineLogger) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			log.Printf("%s %s", l.prefix, line)
		}
	}
	return len(p), nil
}

func toPFPorts(ports []PortRule) []string {
	result := make([]string, 0, len(ports))
	for _, p := range ports {
		result = append(result, fmt.Sprintf("%d:%d", p.Local, p.Remote))
	}
	return result
}
