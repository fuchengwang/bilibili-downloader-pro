package downloader

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

const courseTestKID = "11112222333344445555666677778888"

func encryptedCourseFixture(t *testing.T, scheme string) ([]byte, []byte, [][]byte) {
	t.Helper()
	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	kid, _ := mp4.NewUUIDFromString(courseTestKID)
	iv, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	init := mp4.CreateEmptyInit()
	track := init.AddEmptyTrack(44100, "audio", "und")
	if err := track.SetAACDescriptor(2, 44100); err != nil {
		t.Fatal(err)
	}
	protection, err := mp4.InitProtect(init, key, iv, scheme, kid, nil)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := init.Encode(&output); err != nil {
		t.Fatal(err)
	}
	var expected [][]byte
	for sequence := uint32(1); sequence <= 3; sequence++ {
		fragment, err := mp4.CreateFragment(sequence, 1)
		if err != nil {
			t.Fatal(err)
		}
		for index := uint32(0); index < 7; index++ {
			data := bytes.Repeat([]byte{byte(sequence), byte(index), 0x99, 0x77}, 80)
			expected = append(expected, append([]byte(nil), data...))
			fragment.AddFullSample(mp4.FullSample{Sample: mp4.NewSample(0, 1024, uint32(len(data)), 0), DecodeTime: uint64((sequence-1)*7+index) * 1024, Data: data})
		}
		iv, err = mp4.EncryptFragment(fragment, key, iv, protection)
		if err != nil {
			t.Fatal(err)
		}
		if err := fragment.Encode(&output); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes(), key, expected
}

func TestPrepareCourseStreamDecryptsFragmentsAndPreservesDownload(t *testing.T) {
	for _, scheme := range []string{"cenc", "cbcs"} {
		t.Run(scheme, func(t *testing.T) {
			encrypted, key, expected := encryptedCourseFixture(t, scheme)
			path := filepath.Join(t.TempDir(), "audio.m4s")
			if err := os.WriteFile(path, encrypted, 0600); err != nil {
				t.Fatal(err)
			}
			clearPath, err := prepareCourseStreamContext(context.Background(), path, map[string][]byte{courseTestKID: key})
			if err != nil {
				t.Fatal(err)
			}
			if clearPath == path {
				t.Fatal("encrypted source should be preserved with a separate clear stream")
			}
			original, _ := os.ReadFile(path)
			if !bytes.Equal(original, encrypted) {
				t.Fatal("downloaded stream changed, breaking pause/resume")
			}
			decodedBytes, _ := os.ReadFile(clearPath)
			decoded, err := mp4.DecodeFile(bytes.NewReader(decodedBytes))
			if err != nil {
				t.Fatal(err)
			}
			if entry := decoded.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children[0].Type(); entry != "mp4a" {
				t.Fatalf("protected sample entry remains: %s", entry)
			}
			var samples []mp4.FullSample
			for _, segment := range decoded.Segments {
				for _, fragment := range segment.Fragments {
					part, err := fragment.GetFullSamples(decoded.Moov.Mvex.Trex)
					if err != nil {
						t.Fatal(err)
					}
					samples = append(samples, part...)
				}
			}
			if len(samples) != len(expected) {
				t.Fatalf("samples lost: got %d, want %d", len(samples), len(expected))
			}
			for index, sample := range samples {
				if !bytes.Equal(sample.Data, expected[index]) || sample.DecodeTime != uint64(index)*1024 {
					t.Fatalf("sample %d has incorrect data or timing", index)
				}
			}
			again, err := prepareCourseStreamContext(context.Background(), clearPath, nil)
			if err != nil || again != clearPath {
				t.Fatalf("clear stream should pass through: %s %v", again, err)
			}
		})
	}
}

func TestPrepareCourseStreamRejectsMissingAuthorizationAndCancellation(t *testing.T) {
	encrypted, _, _ := encryptedCourseFixture(t, "cenc")
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.m4s")
	if err := os.WriteFile(path, encrypted, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCourseStreamContext(context.Background(), path, nil); err == nil {
		t.Fatal("encrypted media accepted without account-authorized parameters")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareCourseStreamContext(ctx, path, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, encrypted) {
		t.Fatal("failed preparation changed downloaded media")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("failed preparation left temporary files: %d", len(entries))
	}
}

func TestPrepareCourseStreamInvalidFragmentCleansTemporaryOutput(t *testing.T) {
	encrypted, key, _ := encryptedCourseFixture(t, "cenc")
	decoded, err := mp4.DecodeFile(bytes.NewReader(encrypted))
	if err != nil {
		t.Fatal(err)
	}
	var broken bytes.Buffer
	if err := decoded.Init.Encode(&broken); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Segments[0].Fragments[0].Moof.Encode(&broken); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.m4s")
	if err := os.WriteFile(path, broken.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCourseStreamContext(context.Background(), path, map[string][]byte{courseTestKID: key}); err == nil {
		t.Fatal("truncated course was accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("failed fragment preparation left a partial clear stream")
	}
}
