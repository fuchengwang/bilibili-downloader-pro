package bilibili

import (
	"context"
	"testing"
	"time"
)

func TestPlayURLResolve(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live API call in short mode")
	}
	client := GetDefaultClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	parsed, err := client.ParseInput(ctx, "BV1xx411c7mD")
	if err != nil {
		t.Fatalf("ParseInput failed: %v", err)
	}

	detail, err := client.FetchVideoDetail(ctx, parsed)
	if err != nil {
		t.Fatalf("FetchVideoDetail failed: %v", err)
	}

	ep := detail.Episodes[0]
	t.Logf("Testing CID=%d, AID=%d, BVID=%s", ep.CID, ep.AID, ep.BVID)

	qualities, err := client.GetAvailableQualities(ctx, ep.BVID, ep.AID, ep.CID, 0, false)
	if err != nil {
		t.Fatalf("GetAvailableQualities failed: %v", err)
	}
	t.Logf("Got %d qualities: %+v", len(qualities), qualities)

	sel, err := client.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, 0, false, "highest", "auto")
	if err != nil {
		t.Fatalf("FetchStreamSelection failed: %v", err)
	}
	t.Logf("Stream URL: %s, QN: %d, Label: %s, Duration: %d", sel.VideoURL, sel.QualityID, sel.QualityLabel, sel.Duration)

	// 核心断言：未登录状态下获取的流时长绝不能是 5 秒试看片段！(该视频原长至少数分钟)
	if sel.Duration <= 10 {
		t.Fatalf("严重缺陷：获取到的媒体流时长仅为 %d 秒（疑似被 try_look 截断为试看片段）！", sel.Duration)
	}
}

func TestPickVideoStream_QualityAndCodecs(t *testing.T) {
	videos := []DashStream{
		{ID: 120, Codecid: 7, Bandwidth: 5000000, BaseURL: "http://4k_avc"},
		{ID: 120, Codecid: 12, Bandwidth: 3500000, BaseURL: "http://4k_hevc"},
		{ID: 120, Codecid: 13, Bandwidth: 3000000, BaseURL: "http://4k_av1"},
		{ID: 80, Codecid: 7, Bandwidth: 2000000, BaseURL: "http://1080p_avc"},
		{ID: 80, Codecid: 12, Bandwidth: 1500000, BaseURL: "http://1080p_hevc"},
		{ID: 80, Codecid: 13, Bandwidth: 1200000, BaseURL: "http://1080p_av1"},
		{ID: 32, Codecid: 7, Bandwidth: 500000, BaseURL: "http://480p_avc"},
	}

	// 1. 测试最高画质 + 自动选择 (同画质优先 AVC)
	best := pickVideoStream(videos, "highest", "auto")
	if best == nil || best.ID != 120 || best.Codecid != 7 {
		t.Fatalf("Expected 4K AVC for highest auto, got %+v", best)
	}

	// 2. 测试指定画质 80 (1080P) + HEVC
	v1080Hevc := pickVideoStream(videos, "80", "HEVC")
	if v1080Hevc == nil || v1080Hevc.ID != 80 || v1080Hevc.Codecid != 12 {
		t.Fatalf("Expected 1080P HEVC, got %+v", v1080Hevc)
	}

	// 3. 测试指定画质 80 + AV1
	v1080Av1 := pickVideoStream(videos, "80", "AV1")
	if v1080Av1 == nil || v1080Av1.ID != 80 || v1080Av1.Codecid != 13 {
		t.Fatalf("Expected 1080P AV1, got %+v", v1080Av1)
	}

	// 4. 测试指定画质 32 (480P) + AV1 (显式编码不可用时不能静默改成 AVC)
	v480Fallback := pickVideoStream(videos, "32", "AV1")
	if v480Fallback != nil {
		t.Fatalf("Expected nil when requested codec is unavailable, got %+v", v480Fallback)
	}

	// 5. 空列表防御
	if pickVideoStream(nil, "highest", "auto") != nil {
		t.Fatalf("Expected nil for empty video list")
	}
	if got := pickVideoStream(videos, " 80 ", " avc "); got == nil || got.ID != 80 || got.Codecid != 7 {
		t.Fatalf("quality/codec whitespace and case should be normalized, got %+v", got)
	}
}

func TestAutoCodecPrefersCompatibilityWithoutReducingQuality(t *testing.T) {
	for _, auto := range []string{"", "auto", " AUTO "} {
		videos := []DashStream{
			{ID: 64, Codecid: 13, Bandwidth: 3000000, BaseURL: "https://av1"},
			{ID: 64, Codecid: 12, Bandwidth: 2000000, BaseURL: "https://hevc"},
			{ID: 64, Codecid: 7, Bandwidth: 100000, BaseURL: "https://avc-low"},
			{ID: 64, Codecid: 7, Bandwidth: 200000, BaseURL: "https://avc-best"},
			{ID: 120, Codecid: 7, Bandwidth: 4000000}, // inaccessible quality
		}
		if got := pickVideoStream(videos, "highest", auto); got == nil || got.BaseURL != "https://avc-best" {
			t.Fatalf("auto %q selected incompatible codec by bitrate: %+v", auto, got)
		}
		videos = videos[:2]
		if got := pickVideoStream(videos, "64", auto); got == nil || got.Codecid != 12 {
			t.Fatalf("missing AVC should prefer HEVC over AV1: %+v", got)
		}
		videos = append(videos, DashStream{ID: 120, Codecid: 13, BaseURL: "https://4k-av1"})
		if got := pickVideoStream(videos, "highest", auto); got == nil || got.ID != 120 {
			t.Fatalf("auto must preserve highest available quality: %+v", got)
		}
	}
}

func TestPickVideoStreamRespectsQualityAndBackupURL(t *testing.T) {
	videos := []DashStream{
		{ID: 120, Codecid: 7, Bandwidth: 5000000, BaseURL: "http://4k"},
		{ID: 80, Codecid: 7, Bandwidth: 2000000, BaseURL: "http://1080p"},
		{ID: 64, Codecid: 7, Bandwidth: 1000000, BaseURL: "http://720p"},
	}

	if got := pickVideoStream(videos, "64", "auto"); got == nil || got.ID != 64 {
		t.Fatalf("requested quality must not fall back to a higher stream, got %+v", got)
	}
	if got := pickVideoStream(videos, "32", "auto"); got != nil {
		t.Fatalf("no stream at or below requested quality should return nil, got %+v", got)
	}

	backupOnly := pickVideoStream([]DashStream{
		{ID: 80, Codecid: 7, BackupURL: []string{"http://backup-1080p"}},
	}, "80", "auto")
	if backupOnly == nil {
		t.Fatal("backup_url-only video stream should remain selectable")
	}
}

func TestPickAudioStream_DolbyAndFlac(t *testing.T) {
	dash := &DashData{
		Audio: []DashStream{
			{ID: 30280, Bandwidth: 320000, BaseURL: "http://audio_320k"},
			{ID: 30232, Bandwidth: 132000, BaseURL: "http://audio_132k"},
			{ID: 30216, Bandwidth: 64000, BaseURL: "http://audio_64k"},
		},
	}

	// 1. 标准音轨：选最高码率 320k
	a := pickAudioStream(dash)
	if a == nil || a.Bandwidth != 320000 {
		t.Fatalf("Expected 320k audio, got %+v", a)
	}

	// 2. 包含杜比全景声 (更高码率)
	dash.Dolby.Audio = []DashStream{
		{ID: 30250, Bandwidth: 448000, BaseURL: "http://dolby_audio"},
	}
	aDolby := pickAudioStream(dash)
	if aDolby == nil || aDolby.Bandwidth != 448000 {
		t.Fatalf("Expected Dolby audio with 448k, got %+v", aDolby)
	}

	// 3. 包含无损 Hi-Res FLAC
	dash.Flac = &struct {
		Audio *DashStream `json:"audio"`
	}{
		Audio: &DashStream{ID: 30251, Bandwidth: 1500000, BaseURL: "http://flac_audio"},
	}
	aFlac := pickAudioStream(dash)
	if aFlac == nil || aFlac.Bandwidth != 1500000 {
		t.Fatalf("Expected FLAC audio with 1500k, got %+v", aFlac)
	}
}

func TestPickAudioStream_BackupURLOnly(t *testing.T) {
	dash := &DashData{
		Audio: []DashStream{
			{ID: 30232, Bandwidth: 132000, BaseURL: "http://audio-standard"},
			{ID: 30280, Bandwidth: 320000, BackupURL: []string{"http://audio-backup"}},
		},
	}

	got := pickAudioStream(dash)
	if got == nil || got.Bandwidth != 320000 || len(got.BackupURL) != 1 {
		t.Fatalf("backup_url-only audio should be selected, got %+v", got)
	}
}

func TestCollectSortedCDNsPreservesAPISourceAndNeverFabricatesHosts(t *testing.T) {
	primary := "https://upos-hz-mirrorakam.akamaized.net/media.m4s?signature=primary"
	backup := "https://upos-sz-mirrorcosov.bilivideo.com/media.m4s?signature=backup"
	got := collectSortedCDNs(primary, []string{backup, primary, "", " " + backup + " "})
	if len(got) != 2 || got[0] != primary || got[1] != backup {
		t.Fatalf("regional API source was replaced/reordered: %v", got)
	}
	single := collectSortedCDNs(primary, nil)
	if len(single) != 1 || single[0] != primary {
		t.Fatalf("fabricated an unsigned/unreturned host: %v", single)
	}
}

func TestReplaceCDNServer(t *testing.T) {
	orig := "https://xy123.bilivideo.com/upgcxcode/123.m4s?deadline=1700000000"
	replaced := ReplaceCDNServer(orig, "upos-sz-mirrorcos.bilivideo.com")
	expected := "https://upos-sz-mirrorcos.bilivideo.com/upgcxcode/123.m4s?deadline=1700000000"

	if replaced != expected {
		t.Errorf("ReplaceCDNServer failed: got %s, expected %s", replaced, expected)
	}
}
