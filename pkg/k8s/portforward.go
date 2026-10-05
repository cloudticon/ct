package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

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
)

// portForward starts port forwarding for the selected workload and reconnects on connection loss.
func portForward(ctx context.Context, c *client, selector map[string]string, ports []PortRule) error {
	if c == nil {
		return errors.New("client is required")
	}
	if len(ports) == 0 {
		return errors.New("at least one port rule is required")
	}

	for {
		pod, err := waitForPodFn(ctx, c, selector)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		if err := forwardPortsFn(ctx, c, pod, ports); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("%s connection lost for pod %s, reconnecting: %v", color.YellowString("[port-forward]"), pod, err)
			continue
		}
		return nil
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

	go func() {
		select {
		case <-ctx.Done():
			close(stopCh)
		case <-stopCh:
		}
	}()

	forwarder, err := portforward.New(dialer, toPFPorts(ports), stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("creating port forwarder: %w", err)
	}

	go func() {
		<-readyCh
		for _, p := range ports {
			log.Printf("%s localhost:%d -> %s:%d", color.CyanString("[port-forward]"), p.Local, pod, p.Remote)
		}
	}()

	if err := forwarder.ForwardPorts(); err != nil {
		return fmt.Errorf("forwarding ports for pod %s: %w", pod, err)
	}

	return nil
}

func toPFPorts(ports []PortRule) []string {
	result := make([]string, 0, len(ports))
	for _, p := range ports {
		result = append(result, fmt.Sprintf("%d:%d", p.Local, p.Remote))
	}
	return result
}
