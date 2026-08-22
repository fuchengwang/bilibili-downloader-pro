package bilibili

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseBinaryCookiesFile(filePath string) (map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	if len(data) < 4 || string(data[:4]) != "cook" {
		return nil, os.ErrInvalid
	}

	reader := bytes.NewReader(data[4:])
	var numPages uint32
	if err := binary.Read(reader, binary.BigEndian, &numPages); err != nil {
		return nil, err
	}

	pageSizes := make([]uint32, numPages)
	for i := 0; i < int(numPages); i++ {
		if err := binary.Read(reader, binary.BigEndian, &pageSizes[i]); err != nil {
			return nil, err
		}
	}

	cookies := make(map[string]string)
	pageOffset := 4 + 4 + int(numPages)*4

	for _, pSize := range pageSizes {
		if pageOffset+int(pSize) > len(data) {
			break
		}
		pageData := data[pageOffset : pageOffset+int(pSize)]
		pageOffset += int(pSize)

		if len(pageData) < 8 {
			continue
		}
		// Page header: 0x00000100
		numCookies := binary.LittleEndian.Uint32(pageData[4:8])
		if len(pageData) < 8+int(numCookies)*4 {
			continue
		}

		cookieOffsets := make([]uint32, numCookies)
		for c := 0; c < int(numCookies); c++ {
			cookieOffsets[c] = binary.LittleEndian.Uint32(pageData[8+c*4 : 12+c*4])
		}

		for _, cOff := range cookieOffsets {
			if int(cOff) >= len(pageData) {
				continue
			}
			cData := pageData[cOff:]
			if len(cData) < 48 {
				continue
			}

			urlOff := binary.LittleEndian.Uint32(cData[16:20])
			nameOff := binary.LittleEndian.Uint32(cData[20:24])
			valOff := binary.LittleEndian.Uint32(cData[28:32])

			readCString := func(off uint32) string {
				if int(off) >= len(cData) {
					return ""
				}
				end := int(off)
				for end < len(cData) && cData[end] != 0 {
					end++
				}
				return string(cData[off:end])
			}

			domain := readCString(urlOff)
			name := readCString(nameOff)
			val := readCString(valOff)

			if strings.Contains(domain, "bilibili.com") {
				cookies[name] = val
			}
		}
	}

	return cookies, nil
}

func TestReadSafariCookies(t *testing.T) {
	home, _ := os.UserHomeDir()
	safariPaths := []string{
		filepath.Join(home, "Library/Containers/com.apple.Safari/Data/Library/Cookies/Cookies.binarycookies"),
		filepath.Join(home, "Library/Cookies/Cookies.binarycookies"),
	}

	for _, p := range safariPaths {
		if _, err := os.Stat(p); err != nil {
			t.Logf("Safari path not found: %s", p)
			continue
		}
		t.Logf("Found Safari cookie file: %s", p)
		cookies, err := parseBinaryCookiesFile(p)
		t.Logf("parseBinaryCookiesFile err: %v, total bilibili cookies found: %d", err, len(cookies))
		for k, v := range cookies {
			masked := v
			if len(v) > 8 {
				masked = v[:3] + "..." + v[len(v)-3:]
			}
			t.Logf("  Safari Cookie: %s = %s", k, masked)
		}
	}
}
