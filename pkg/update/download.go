package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Progress struct {
	Downloaded  int64
	Total       int64
	SourceName  string
	SourceIndex int // Zero-based. Downloaded resets when switching mirrors.
	SourceCount int
}

type DownloadOptions struct {
	Directory  string         // Empty: system cache directory, separated by AppID.
	OnProgress func(Progress) // Called on the downloading goroutine; never touches UI.
	Resume     bool           // Retain interrupted bytes and resume using verified HTTP ranges.
}

// Download writes into a unique directory, verifies length and SHA-256, then
// renames the partial file. It never overwrites the application or user data.
// Cancel the supplied context to stop. By default incomplete files are removed;
// Resume retains interrupted bytes and verifies the complete package before use.
func (c *Client) Download(ctx context.Context, release Release, opts DownloadOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := c.validateRelease(release); err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.downloading {
		c.mu.Unlock()
		return "", ErrDownloadInProgress
	}
	c.downloading = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.downloading = false; c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.DownloadTimeout)
	defer cancel()
	if opts.Resume {
		return c.downloadResumable(ctx, release, opts)
	}
	directory := opts.Directory
	if directory == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(cache, "go-apps-updates", c.cfg.AppID)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(directory, "v"+release.Version+"-")
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(work)
		}
	}()
	partial := filepath.Join(work, ".package.part")
	var failures []error
	for i, source := range release.Artifact.Sources {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		err := c.downloadSource(ctx, release.Artifact, source, i, partial, opts.OnProgress)
		if err != nil {
			failures = append(failures, fmt.Errorf("source %d (%s): %w", i+1, source.Name, err))
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		destination := filepath.Join(work, release.Artifact.FileName)
		if err := os.Rename(partial, destination); err != nil {
			return "", err
		}
		complete = true
		return destination, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("all update sources failed: %w", errors.Join(failures...))
}

func (c *Client) downloadSource(ctx context.Context, a Artifact, source Source, index int, partial string, callback func(Progress)) error {
	progress := Progress{Total: a.Size, SourceName: source.Name, SourceIndex: index, SourceCount: len(a.Sources)}
	if callback != nil {
		callback(progress)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.downloadHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errors.New("unexpected content encoding")
	}
	if resp.ContentLength >= 0 && resp.ContentLength != a.Size {
		return errors.New("package length differs from published size")
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	reader := io.LimitReader(resp.Body, a.Size+1)
	buffer := make([]byte, 64<<10)
	lastProgress := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			progress.Downloaded += int64(n)
			if progress.Downloaded > a.Size {
				return errors.New("package exceeds published size")
			}
			if _, err := f.Write(buffer[:n]); err != nil {
				return err
			}
			_, _ = hash.Write(buffer[:n])
			if callback != nil && time.Since(lastProgress) >= 200*time.Millisecond {
				callback(progress)
				lastProgress = time.Now()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			break
		}
	}
	if progress.Downloaded != a.Size {
		return errors.New("incomplete update package")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), a.SHA256) {
		return errors.New("package SHA-256 mismatch")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if callback != nil {
		callback(progress)
	}
	return nil
}
