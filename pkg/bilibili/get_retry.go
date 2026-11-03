package bilibili

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

// Retries belong to the read-only request boundary. Never replay a login POST
// or retry Bilibili business/permission errors decoded by callers.
func (c *Client) getBytesWithRetry(ctx context.Context, req *http.Request) ([]byte, error) {
	ctx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		resp, err := c.httpClient.Do(req.Clone(tryCtx))
		delay := time.Duration(1<<attempt) * 300 * time.Millisecond
		if err != nil {
			cancel()
			last = err
			if !retryableGETError(err) {
				return nil, err
			}
		} else {
			c.UpdateClockOffset(resp.Header.Get("Date"))
			status := resp.StatusCode
			if status >= 200 && status < 400 {
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				cancel()
				if readErr == nil {
					return body, nil
				}
				last = readErr
				if !retryableGETError(readErr) {
					return nil, readErr
				}
			} else {
				last = fmt.Errorf("HTTP error: %s", resp.Status)
				if status == 429 || status == 503 {
					if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
						delay = time.Duration(max(0, min(seconds, 5))) * time.Second
					}
				}
				_ = resp.Body.Close()
				cancel()
				if status != 429 && (status < 500 || status > 599) {
					return nil, last
				}
			}
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, last
}

func retryableGETError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var network net.Error
	return errors.As(err, &network) && (network.Timeout() || network.Temporary())
}
