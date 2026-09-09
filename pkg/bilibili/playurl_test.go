package bilibili

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPlayURLResolve(t *testing.T) {
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
	t.Logf("Stream URL: %s, QN: %d, Label: %s", sel.VideoURL, sel.QualityID, sel.QualityLabel)
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

	// 1. 测试最高画质 + 智能优选 (默认选最高 ID 最高带宽)
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

	// 4. 测试指定画质 32 (480P) + AV1 (无 480P AV1 时自动平滑回退至可用最高编码)
	v480Fallback := pickVideoStream(videos, "32", "AV1")
	if v480Fallback == nil || v480Fallback.ID != 32 || v480Fallback.Codecid != 7 {
		t.Fatalf("Expected 480P fallback to AVC, got %+v", v480Fallback)
	}

	// 5. 空列表防御
	if pickVideoStream(nil, "highest", "auto") != nil {
		t.Fatalf("Expected nil for empty video list")
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

func TestCollectSortedCDNs_BackbonePriorityAndPCDNDemotion(t *testing.T) {
	baseURL := "https://upos-tf-all-mcdn.bilivideo.com/upgcxcode/test.m4s" // PCDN / 边缘节点
	backupURLs := []string{
		"https://112.25.12.3:8443/upgcxcode/test.m4s",                    // 带显式端口的 PCDN
		"https://upos-sz-mirrorcos.bilivideo.com/upgcxcode/test.m4s",       // 顶级腾讯云骨干 CDN
		"https://upos-sz-mirrorali.bilivideo.com/upgcxcode/test.m4s",       // 顶级阿里云骨干 CDN
		"https://cn-gdgz-cmcc.bilivideo.com/upgcxcode/test.m4s",            // 普通 CDN
	}

	sorted := collectSortedCDNs(baseURL, backupURLs)
	if len(sorted) != 5 {
		t.Fatalf("Expected 5 unique candidates, got %d: %+v", len(sorted), sorted)
	}

	// 顶级骨干网必须排在最前
	if !strings.Contains(sorted[0], "upos-sz-mirrorcos") {
		t.Errorf("First CDN should be mirrorcos, got %s", sorted[0])
	}
	if !strings.Contains(sorted[1], "upos-sz-mirrorali") {
		t.Errorf("Second CDN should be mirrorali, got %s", sorted[1])
	}

	// 普通 CDN 排在中间
	if !strings.Contains(sorted[2], "cn-gdgz-cmcc") {
		t.Errorf("Third CDN should be regular CDN, got %s", sorted[2])
	}

	// PCDN / 显式端口必须沉底排在最后
	lastTwo := strings.Join(sorted[3:], " ")
	if !strings.Contains(lastTwo, "mcdn") || !strings.Contains(lastTwo, ":8443") {
		t.Errorf("PCDN and port URLs should be demoted to end, got %+v", sorted[3:])
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
