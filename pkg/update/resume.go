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
	"strconv"
	"strings"
	"time"
)

var errRestartPackage = errors.New("update representation changed or failed integrity checking")

// Resumption is opt-in. Each source has its own partial file; bytes from different
// mirrors are never concatenated. The published hash is the final authority.
func (c *Client) downloadResumable(ctx context.Context, release Release, opts DownloadOptions) (string, error) {
	root := opts.Directory
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cache, "go-apps-updates", c.cfg.AppID)
	}
	a := release.Artifact
	work := filepath.Join(root, "v"+release.Version+"-"+strings.ToLower(a.SHA256))
	if err := os.MkdirAll(work, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(work)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe update cache directory")
	}
	unlock, err := lockResume(filepath.Join(work, ".download.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	// Keep publisher-controlled filenames separate from locks and partials.
	packageDir := filepath.Join(work, "package")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		return "", err
	}
	if info, err := os.Lstat(packageDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe update package directory")
	}
	finished := filepath.Join(packageDir, a.FileName)
	if err := verifyCached(finished, a); err == nil {
		if opts.OnProgress != nil {
			opts.OnProgress(Progress{Downloaded: a.Size, Total: a.Size, SourceCount: len(a.Sources)})
		}
		return finished, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		if err := os.Remove(finished); err != nil {
			return "", err
		}
	}
	var failures []error
	for i, source := range a.Sources {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		identity := sha256.Sum256([]byte(source.URL))
		partial := filepath.Join(work, hex.EncodeToString(identity[:16])+".part")
		for attempt := 0; attempt < 2; attempt++ {
			err = c.resumeSource(ctx, a, source, i, partial, opts.OnProgress)
			if err == nil {
				if err = ctx.Err(); err != nil {
					return "", err
				}
				if err = os.Rename(partial, finished); err != nil {
					return "", err
				}
				return finished, nil
			}
			if !errors.Is(err, errRestartPackage) {
				break
			}
			if removeErr := os.Remove(partial); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return "", removeErr
			}
		}
		failures = append(failures, fmt.Errorf("source %d (%s): %w", i+1, source.Name, err))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("all update sources failed: %w", errors.Join(failures...))
}

func verifyCached(path string, a Artifact) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Size {
		return errRestartPackage
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), a.SHA256) {
		return errRestartPackage
	}
	return nil
}

func (c *Client) resumeSource(ctx context.Context, a Artifact, source Source, index int, partial string, callback func(Progress)) error {
	offset := int64(0)
	if info, err := os.Lstat(partial); err == nil {
		if !info.Mode().IsRegular() || info.Size() > a.Size {
			return errRestartPackage
		}
		offset = info.Size()
		if offset == a.Size {
			return verifyCached(partial, a)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := c.downloadHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return errRestartPackage
	}
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0 // A server ignoring Range must replace, never append, the partial file.
	case http.StatusPartialContent:
		if !validContentRange(resp.Header.Get("Content-Range"), offset, a.Size) {
			return errRestartPackage
		}
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return errors.New("unexpected content encoding")
	}
	if resp.ContentLength >= 0 && resp.ContentLength != a.Size-offset {
		return errRestartPackage
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if offset == 0 {
		if err := f.Truncate(0); err != nil {
			return err
		}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	progress := Progress{Downloaded: offset, Total: a.Size, SourceName: source.Name, SourceIndex: index, SourceCount: len(a.Sources)}
	emit := func() {
		if callback != nil {
			callback(progress)
		}
	}
	emit()
	lastProgress := time.Now()
	reader := io.LimitReader(resp.Body, a.Size-offset+1)
	buffer := make([]byte, 64<<10)
	var downloadErr error
	for {
		if err := ctx.Err(); err != nil {
			downloadErr = err
			break
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if progress.Downloaded+int64(n) > a.Size {
				downloadErr = errRestartPackage
				break
			}
			written, writeErr := f.Write(buffer[:n])
			progress.Downloaded += int64(written)
			if writeErr != nil {
				downloadErr = writeErr
				break
			}
			if written != n {
				downloadErr = io.ErrShortWrite
				break
			}
			if time.Since(lastProgress) >= 200*time.Millisecond {
				emit()
				lastProgress = time.Now()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				downloadErr = readErr
			}
			break
		}
	}
	if syncErr := f.Sync(); syncErr != nil {
		return syncErr
	}
	if closeErr := f.Close(); closeErr != nil {
		return closeErr
	}
	emit()
	if downloadErr != nil {
		return downloadErr
	}
	if progress.Downloaded != a.Size {
		return io.ErrUnexpectedEOF
	}
	return verifyCached(partial, a)
}

func validContentRange(value string, offset, total int64) bool {
	if !strings.HasPrefix(value, "bytes ") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes "), "/")
	if len(parts) != 2 {
		return false
	}
	rangeParts := strings.Split(parts[0], "-")
	if len(rangeParts) != 2 {
		return false
	}
	start, e1 := strconv.ParseInt(rangeParts[0], 10, 64)
	end, e2 := strconv.ParseInt(rangeParts[1], 10, 64)
	size, e3 := strconv.ParseInt(parts[1], 10, 64)
	return e1 == nil && e2 == nil && e3 == nil && start == offset && end == total-1 && size == total
}
