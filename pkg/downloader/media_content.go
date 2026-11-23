package downloader

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const mp4PrefixSize = 16

func isJSONMediaType(resp *http.Response) bool {
	contentType, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "application/json", "text/json":
		return true
	}
	return false
}

func nonMediaTypeError(resp *http.Response) error {
	return fmt.Errorf("%w: %s", errNonMediaResponse, resp.Header.Get("Content-Type"))
}

type replayMediaPrefix struct {
	io.Reader
	io.Closer
}

func (s *StreamDownloader) verifyMP4Prefix(resp *http.Response, total int64) error {
	body := resp.Body
	var prefix [mp4PrefixSize]byte
	if _, err := io.ReadFull(s.watchResponse(body, resp.ContentLength, false), prefix[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nonMediaTypeError(resp)
		}
		return err
	}
	// An ISO BMFF file type box starts with its size and type, followed by
	// a four-byte major brand and version. Check that it fits in the entity.
	boxSize := int64(binary.BigEndian.Uint32(prefix[:4]))
	if string(prefix[4:8]) != "ftyp" || boxSize < mp4PrefixSize || boxSize%4 != 0 || boxSize > total {
		return nonMediaTypeError(resp)
	}
	for _, c := range prefix[8:12] {
		if c < ' ' || c > '~' {
			return nonMediaTypeError(resp)
		}
	}
	resp.Body = replayMediaPrefix{Reader: io.MultiReader(bytes.NewReader(prefix[:]), body), Closer: body}
	return nil
}

type mediaTypeProof struct {
	etag  string
	total int64
}

func mediaTypeIdentity(resp *http.Response, requestedURL string, total int64) (string, mediaTypeProof) {
	key := responseValidatorKey(resp)
	if key == "" {
		key = mediaValidatorKey(requestedURL)
	}
	return key, mediaTypeProof{etag: resp.Header.Get("ETag"), total: total}
}

func (p mediaTypeProof) valid() bool {
	return p.total > 0 && len(p.etag) >= 2 && p.etag[0] == '"' && p.etag[len(p.etag)-1] == '"'
}

// Some CDNs mark ISO BMFF bytes as JSON, including on valid 206 responses.
// A nonzero range has no file header, so require proof of the same entity
// from that origin/path before accepting bytes with the incorrect label.
func (s *StreamDownloader) verifyMislabeledMedia(ctx context.Context, resp *http.Response, rawURL string, start, total int64) error {
	key, proof := mediaTypeIdentity(resp, rawURL, total)
	if start == 0 {
		if err := s.verifyMP4Prefix(resp, total); err != nil {
			return err
		}
	} else {
		if !proof.valid() {
			return nonMediaTypeError(resp)
		}
		s.mediaTypeMu.Lock()
		known := s.verifiedMediaTypes[key] == proof
		s.mediaTypeMu.Unlock()
		if known {
			return nil
		}
		// The caller already holds a connection slot. The exceptional
		// request reads only the file prefix and uses the same signed URL.
		probe, err := s.probeMediaPrefix(ctx, rawURL, mp4PrefixSize-1, true)
		if err != nil {
			return err
		}
		probeKey, probeProof := mediaTypeIdentity(probe.response, rawURL, probe.total)
		if probeKey != key || probeProof != proof {
			return nonMediaTypeError(resp)
		}
	}
	if proof.valid() {
		s.mediaTypeMu.Lock()
		if s.verifiedMediaTypes == nil {
			s.verifiedMediaTypes = make(map[string]mediaTypeProof)
		}
		s.verifiedMediaTypes[key] = proof
		s.mediaTypeMu.Unlock()
	}
	return nil
}
