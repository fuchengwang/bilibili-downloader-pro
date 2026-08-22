package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"bilibili_downloader/pkg/config"
)

// EnsureFFmpeg 确保系统拥有可用 FFmpeg，若本地完全缺失则自动从 CDN 静默高速下载部署
func EnsureFFmpeg(ctx context.Context, onStatus func(msg string)) (string, error) {
	// 1. 先检测系统已有路径
	existing := config.DetectFFmpeg()
	if existing != "" {
		return existing, nil
	}

	// 2. 准备目标路径
	binDir := filepath.Join(config.GetConfigDir(), "bin")
	_ = os.MkdirAll(binDir, 0755)

	var binaryName string
	var downloadURLs []string

	if runtime.GOOS == "windows" {
		binaryName = "ffmpeg.exe"
		downloadURLs = []string{
			"https://ghproxy.net/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-win32-x64",
			"https://mirror.ghproxy.com/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-win32-x64",
			"https://ghfast.top/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-win32-x64",
			"https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-win32-x64",
		}
	} else if runtime.GOOS == "darwin" {
		binaryName = "ffmpeg"
		downloadURLs = []string{
			"https://ghproxy.net/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-darwin-arm64",
			"https://mirror.ghproxy.com/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-darwin-arm64",
			"https://ghfast.top/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-darwin-arm64",
			"https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-darwin-arm64",
		}
	} else {
		binaryName = "ffmpeg"
		downloadURLs = []string{
			"https://ghproxy.net/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-linux-x64",
			"https://mirror.ghproxy.com/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-linux-x64",
			"https://ghfast.top/https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-linux-x64",
			"https://github.com/eugeneware/ffmpeg-static/releases/download/b6.0/ffmpeg-linux-x64",
		}
	}

	targetPath := filepath.Join(binDir, binaryName)
	if fi, err := os.Stat(targetPath); err == nil && fi.Size() > 1024*1024 {
		if out, err := exec.Command(targetPath, "-version").Output(); err == nil && len(out) > 0 {
			_ = config.GetManager().UpdateFFmpegPath(targetPath)
			return targetPath, nil
		}
	}

	if onStatus != nil {
		onStatus("正在自动部署视频合成引擎...")
	}

	client := &http.Client{Timeout: 3 * time.Minute}
	var lastErr error

	for _, dURL := range downloadURLs {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %s", resp.Status)
			continue
		}

		tmpTarget := targetPath + ".downloading"
		file, err := os.OpenFile(tmpTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			resp.Body.Close()
			lastErr = err
			continue
		}

		_, copyErr := io.Copy(file, resp.Body)
		file.Close()
		resp.Body.Close()

		if copyErr != nil {
			_ = os.Remove(tmpTarget)
			lastErr = copyErr
			continue
		}

		_ = os.Chmod(tmpTarget, 0755)
		if err := os.Rename(tmpTarget, targetPath); err != nil {
			_ = os.Remove(tmpTarget)
			lastErr = err
			continue
		}

		_ = os.Chmod(targetPath, 0755)
		_ = config.GetManager().UpdateFFmpegPath(targetPath)
		return targetPath, nil
	}

	return "", fmt.Errorf("未找到 FFmpeg 且自动下载部署失败: %v", lastErr)
}

// MergeAudioVideo 使用 FFmpeg 将音视频临时流快速无损合并为标准 MP4 文件
func MergeAudioVideo(ffmpegPath, videoPath, audioPath, outputPath string, deleteTemp bool) error {
	_ = os.MkdirAll(filepath.Dir(outputPath), 0755)

	if ffmpegPath == "" {
		if audioPath == "" || !fileExists(audioPath) {
			return os.Rename(videoPath, outputPath)
		}
		return fmt.Errorf("未找到 FFmpeg，无法合成音视频轨")
	}

	var args []string
	if audioPath != "" && fileExists(audioPath) {
		args = []string{
			"-y",
			"-i", videoPath,
			"-i", audioPath,
			"-c:v", "copy",
			"-c:a", "copy",
			"-strict", "experimental",
			outputPath,
		}
	} else {
		args = []string{
			"-y",
			"-i", videoPath,
			"-c", "copy",
			outputPath,
		}
	}

	cmd := exec.Command(ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("FFmpeg 合成失败: %s (error: %w)", string(out), err)
	}

	// 合成成功后清理临时分块文件
	if deleteTemp {
		_ = os.Remove(videoPath)
		if audioPath != "" {
			_ = os.Remove(audioPath)
		}
	}

	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
