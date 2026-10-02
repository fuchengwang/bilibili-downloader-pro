package license

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"bilibili_downloader/pkg/utils"
)

var (
	ErrNoLicenseFound    = errors.New("no local license found")
	ErrLicenseTampered   = errors.New("license file tampered or device mismatch")
	ErrTimeClockRollback = errors.New("system clock rollback detected")
)

type Storage struct {
	filePath  string
	cryptoKey []byte
}

func NewStorage(appID, deviceID, customPath string) (*Storage, error) {
	var targetPath string
	if customPath != "" {
		targetPath = customPath
	} else {
		configDir, err := os.UserConfigDir()
		if err != nil {
			configDir, err = os.UserHomeDir()
			if err != nil {
				configDir = "."
			}
		}
		appDir := filepath.Join(configDir, "go-apps-licenses", appID)
		if err := os.MkdirAll(appDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create license dir: %w", err)
		}
		targetPath = filepath.Join(appDir, "license.dat")
	}

	// 派生 AES-256 密钥: SHA256(deviceID + appID + salt)
	hasher := sha256.New()
	hasher.Write([]byte(deviceID + ":" + appID + ":$__GO_LICENSE_SECURE_SALT_v1__$"))
	key := hasher.Sum(nil)

	return &Storage{
		filePath:  targetPath,
		cryptoKey: key,
	}, nil
}

// Save 保存并加密授权凭证
func (s *Storage) Save(token *LicenseToken) error {
	if token == nil {
		return errors.New("license token is nil")
	}
	copyToken := *token
	copyToken.LastSeenTime = time.Now().Unix()
	data, err := json.Marshal(&copyToken)
	if err != nil {
		return fmt.Errorf("failed to marshal token: %w", err)
	}

	encrypted, err := s.encrypt(data)
	if err != nil {
		return fmt.Errorf("failed to encrypt license: %w", err)
	}

	// 使用同目录临时文件并事务性替换；Windows 不能直接 Rename 覆盖已有文件，
	// 通用原子写入实现会在替换失败时保留/恢复旧凭证。
	return utils.AtomicWriteFile(s.filePath, encrypted, 0600)
}

// Load 读取并解密授权凭证
func (s *Storage) Load(expectedDeviceID string) (*LicenseToken, error) {
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		return nil, ErrNoLicenseFound
	}

	encrypted, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read license file: %w", err)
	}

	decrypted, err := s.decrypt(encrypted)
	if err != nil {
		return nil, ErrLicenseTampered
	}

	var token LicenseToken
	if err := json.Unmarshal(decrypted, &token); err != nil {
		return nil, ErrLicenseTampered
	}

	// 校验绑定的设备指纹是否与本机一致
	if token.DeviceID != expectedDeviceID {
		return nil, ErrLicenseTampered
	}

	// 防回拨系统时间校验 (允许正常系统微小误差 1 小时以内)
	now := time.Now().Unix()
	if !token.IsPermanent && token.LastSeenTime > now+3600 {
		return nil, ErrTimeClockRollback
	}

	// Reading must not rewrite a stale token over a concurrent activation or deletion.

	return &token, nil
}

// Delete 清理本地授权信息（解绑时使用）
func (s *Storage) Delete() error {
	err := os.Remove(s.filePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// GetFilePath 获取当前授权存储文件绝对路径
func (s *Storage) GetFilePath() string {
	return s.filePath
}

// encrypt AES-256-GCM 加密
func (s *Storage) encrypt(plainText []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.cryptoKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	cipherText := gcm.Seal(nonce, nonce, plainText, nil)
	return cipherText, nil
}

// decrypt AES-256-GCM 解密
func (s *Storage) decrypt(cipherText []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.cryptoKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(cipherText) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, actualCipher := cipherText[:nonceSize], cipherText[nonceSize:]
	plainText, err := gcm.Open(nil, nonce, actualCipher, nil)
	if err != nil {
		return nil, err
	}

	return plainText, nil
}
