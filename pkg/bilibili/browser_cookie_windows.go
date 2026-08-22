//go:build windows

package bilibili

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	_ "github.com/mattn/go-sqlite3"
)

var (
	dllCrypt32             = syscall.NewLazyDLL("crypt32.dll")
	dllKernel32            = syscall.NewLazyDLL("kernel32.dll")
	procCryptUnprotectData = dllCrypt32.NewProc("CryptUnprotectData")
	procLocalFree          = dllKernel32.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func decryptDPAPI(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty input for DPAPI")
	}

	var inBlob dataBlob
	inBlob.cbData = uint32(len(data))
	inBlob.pbData = &data[0]

	var outBlob dataBlob
	r, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&outBlob)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData failed: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.pbData)))

	decrypted := make([]byte, outBlob.cbData)
	copy(decrypted, unsafe.Slice(outBlob.pbData, outBlob.cbData))
	return decrypted, nil
}

func getWindowsMasterKey(localStatePath string) ([]byte, error) {
	data, err := os.ReadFile(localStatePath)
	if err != nil {
		return nil, err
	}

	var localState struct {
		OsCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}

	if err := json.Unmarshal(data, &localState); err != nil {
		return nil, err
	}

	if localState.OsCrypt.EncryptedKey == "" {
		return nil, fmt.Errorf("encrypted_key is empty")
	}

	encryptedKey, err := base64.StdEncoding.DecodeString(localState.OsCrypt.EncryptedKey)
	if err != nil {
		return nil, err
	}

	if len(encryptedKey) < 5 || string(encryptedKey[:5]) != "DPAPI" {
		return nil, fmt.Errorf("invalid DPAPI prefix in encrypted_key")
	}

	rawKey := encryptedKey[5:]
	return decryptDPAPI(rawKey)
}

func decryptWindowsCookie(encVal []byte, masterKey []byte) string {
	if len(encVal) == 0 {
		return ""
	}

	// 1. Chrome 80+ AES-GCM (v10 / v11)
	if len(encVal) >= 31 && (string(encVal[:3]) == "v10" || string(encVal[:3]) == "v11") {
		if len(masterKey) == 0 {
			return ""
		}
		nonce := encVal[3:15]
		ciphertext := encVal[15:]

		block, err := aes.NewCipher(masterKey)
		if err != nil {
			return ""
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return ""
		}
		plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			return ""
		}
		return string(plaintext)
	}

	// 2. Legacy DPAPI
	dec, err := decryptDPAPI(encVal)
	if err == nil && len(dec) > 0 {
		return string(dec)
	}

	return ""
}

// readWindowsChromiumUserData 读取指定 Chromium User Data 目录下所有 Profile 的 B 站 Cookie
func readWindowsChromiumUserData(userDataDir string) map[string]string {
	localStatePath := filepath.Join(userDataDir, "Local State")
	masterKey, _ := getWindowsMasterKey(localStatePath)

	entries, err := os.ReadDir(userDataDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		if dirName == "Default" || strings.HasPrefix(dirName, "Profile") || dirName == "Guest Profile" {
			profileDir := filepath.Join(userDataDir, dirName)
			if ck := readWindowsProfileCookies(profileDir, masterKey); ck != nil && ck["SESSDATA"] != "" {
				return ck
			}
		}
	}

	return nil
}

func readWindowsProfileCookies(profileDir string, masterKey []byte) map[string]string {
	cookiePath := filepath.Join(profileDir, "Network", "Cookies")
	if _, err := os.Stat(cookiePath); err != nil {
		cookiePath = filepath.Join(profileDir, "Cookies")
		if _, err := os.Stat(cookiePath); err != nil {
			return nil
		}
	}

	tmpFile, err := os.CreateTemp("", "win_bili_cookie_*.db")
	if err != nil {
		return nil
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	src, err := os.Open(cookiePath)
	if err != nil {
		return nil
	}
	dst, err := os.Create(tmpPath)
	if err != nil {
		src.Close()
		return nil
	}
	_, _ = io.Copy(dst, src)
	src.Close()
	dst.Close()

	db, err := sql.Open("sqlite3", tmpPath+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query("SELECT name, encrypted_value, value FROM cookies WHERE host_key LIKE '%bilibili.com%'")
	if err != nil {
		return nil
	}
	defer rows.Close()

	cookies := make(map[string]string)
	for rows.Next() {
		var name, val string
		var encVal []byte
		if err := rows.Scan(&name, &encVal, &val); err != nil {
			continue
		}
		if val != "" {
			cookies[name] = val
		} else if len(encVal) > 0 {
			dec := decryptWindowsCookie(encVal, masterKey)
			if dec != "" {
				cookies[name] = dec
			}
		}
	}

	if cookies["SESSDATA"] != "" {
		return cookies
	}
	return nil
}

// readPlatformBrowserCookies 平台特定的 Windows 浏览器遍历实现
func readPlatformBrowserCookies() (map[string]string, error) {
	localApp := os.Getenv("LOCALAPPDATA")
	appData := os.Getenv("APPDATA")

	// 1. Google Chrome
	if ck := readWindowsChromiumUserData(filepath.Join(localApp, `Google\Chrome\User Data`)); ck != nil {
		return ck, nil
	}

	// 2. Microsoft Edge
	if ck := readWindowsChromiumUserData(filepath.Join(localApp, `Microsoft\Edge\User Data`)); ck != nil {
		return ck, nil
	}

	// 3. Brave Browser
	if ck := readWindowsChromiumUserData(filepath.Join(localApp, `BraveSoftware\Brave-Browser\User Data`)); ck != nil {
		return ck, nil
	}

	// 4. Vivaldi
	if ck := readWindowsChromiumUserData(filepath.Join(localApp, `Vivaldi\User Data`)); ck != nil {
		return ck, nil
	}

	// 5. 360 极速浏览器 / 360 安全浏览器
	if ck := readWindowsChromiumUserData(filepath.Join(localApp, `360Chrome\Chrome\User Data`)); ck != nil {
		return ck, nil
	}
	if ck := readWindowsChromiumUserData(filepath.Join(appData, `360se6\User Data`)); ck != nil {
		return ck, nil
	}

	// 6. QQ 浏览器
	if ck := readWindowsChromiumUserData(filepath.Join(localApp, `Tencent\QQBrowser\User Data`)); ck != nil {
		return ck, nil
	}

	// 7. CentBrowser
	if ck := readWindowsChromiumUserData(filepath.Join(localApp, `CentBrowser\User Data`)); ck != nil {
		return ck, nil
	}

	// 8. Opera / Opera GX
	if ck := readWindowsChromiumUserData(filepath.Join(appData, `Opera Software\Opera Stable`)); ck != nil {
		return ck, nil
	}
	if ck := readWindowsChromiumUserData(filepath.Join(appData, `Opera Software\Opera GX Stable`)); ck != nil {
		return ck, nil
	}

	// 9. Mozilla Firefox
	if ffCk := readFirefoxCookies(filepath.Join(appData, `Mozilla\Firefox\Profiles`)); ffCk != nil && ffCk["SESSDATA"] != "" {
		return ffCk, nil
	}

	return nil, fmt.Errorf("未能从任何 Windows 浏览器（Chrome/Edge/Brave/Firefox/360等）检测到已登录的 B 站账号，请在浏览器中登录 B 站后再试")
}
