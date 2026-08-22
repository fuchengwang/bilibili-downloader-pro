package downloader

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// MergeAudioVideo 使用 FFmpeg 将音视频临时流快速无损合并为标准 MP4 文件
func MergeAudioVideo(ffmpegPath, videoPath, audioPath, outputPath string, deleteTemp bool) error {
	_ = os.MkdirAll(filepath.Dir(outputPath), 0755)

	if ffmpegPath == "" {
		// 尝试如果无音频流，直接重命名
		if audioPath == "" || !fileExists(audioPath) {
			return os.Rename(videoPath, outputPath)
		}
		return fmt.Errorf("未找到 FFmpeg，无法合成音视频轨，请在设置中配置 FFmpeg 路径")
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
	return err == nil && !info.IsDir() && info.Size() > 0
}
