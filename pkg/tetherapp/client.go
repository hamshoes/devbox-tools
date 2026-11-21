package tetherapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	clientsMu sync.Mutex
	clients   = map[string]*http.Client{}
)

// unixClient returns a shared client that dials socket.
func unixClient(socket string) *http.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[socket]; ok {
		return c
	}
	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}}
	clients[socket] = c
	return c
}

// Post sends body to method (e.g. "/ping.v1.PingEvents/RequestSeen") on the
// unix socket and returns the response body. A deadline on ctx travels as
// Connect-Timeout-Ms. Every failure is an *Error.
func Post(ctx context.Context, socket, method string, body []byte) ([]byte, error) {
	method = normalize(method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://tether"+method, bytes.NewReader(body))
	if err != nil {
		return nil, Errorf(CodeInvalidArgument, "%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if dl, ok := ctx.Deadline(); ok {
		req.Header.Set("Connect-Timeout-Ms", strconv.FormatInt(max(time.Until(dl).Milliseconds(), 1), 10))
	}
	resp, err := unixClient(socket).Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return nil, Errorf(CodeDeadlineExceeded, "%v", err)
		case errors.Is(err, context.Canceled):
			return nil, Errorf(CodeCanceled, "%v", err)
		}
		return nil, Errorf(CodeUnavailable, "%v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, Errorf(CodeUnavailable, "read response: %v", err)
	}
	if len(data) > MaxBody {
		return nil, Errorf(CodeInternal, "response exceeds %d bytes", MaxBody)
	}
	if resp.StatusCode != http.StatusOK {
		var e Error
		if json.Unmarshal(data, &e) != nil || e.Code == "" {
			return nil, Errorf(CodeInternal, "http %d", resp.StatusCode)
		}
		return nil, &e
	}
	return data, nil
}

// Call calls method on the gateway at gwSocket. The error, if any, is an *Error.
func Call(ctx context.Context, gwSocket, method string, req, resp proto.Message) error {
	body, err := protojson.Marshal(req)
	if err != nil {
		return Errorf(CodeInvalidArgument, "%v", err)
	}
	data, err := Post(ctx, gwSocket, method, body)
	if err != nil {
		return err
	}
	if err := protojson.Unmarshal(data, resp); err != nil {
		return Errorf(CodeInternal, "decode response: %v", err)
	}
	return nil
}

func normalize(method string) string {
	if !strings.HasPrefix(method, "/") {
		return "/" + method
	}
	return method
}
