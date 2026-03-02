package browser

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

type ChromeInstaller struct {
	platform string
	arch     string
}

func NewChromeInstaller() *ChromeInstaller {
	return &ChromeInstaller{
		platform: runtime.GOOS,
		arch:     runtime.GOARCH,
	}
}

func (ci *ChromeInstaller) getDownloadURL() (string, error) {
	chromeForTestingURL := "https://storage.googleapis.com/chrome-for-testing-public/%s/%s/chrome-%s.zip"

	versions := map[string]string{
		"linux":   "146.0.7680.31",
		"windows": "146.0.7680.31",
		"darwin":  "146.0.7680.31",
	}

	platforms := map[string]string{
		"linux":   "linux64",
		"windows": "win64",
		"darwin":  "mac-x64",
	}

	version, ok := versions[ci.platform]
	if !ok {
		return "", fmt.Errorf("unsupported platform: %s", ci.platform)
	}

	platform, ok := platforms[ci.platform]
	if !ok {
		return "", fmt.Errorf("unsupported platform: %s", ci.platform)
	}

	return fmt.Sprintf(chromeForTestingURL, version, platform, platform), nil
}

func (ci *ChromeInstaller) getInstallPath() string {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = os.Getenv("LOCALAPPDATA")
		if homeDir == "" {
			homeDir = "/tmp"
		}
	}
	return filepath.Join(homeDir, ".expeditus", "chromium")
}

func (ci *ChromeInstaller) getChromeBinaryPath() string {
	installPath := ci.getInstallPath()
	switch ci.platform {
	case "linux":
		return filepath.Join(installPath, "chrome-linux64", "chrome")
	case "windows":
		return filepath.Join(installPath, "chrome-win64", "chrome.exe")
	case "darwin":
		return filepath.Join(installPath, "chrome-mac-x64", "Google Chrome.app", "Contents", "MacOS", "Google Chrome")
	default:
		return ""
	}
}

func (ci *ChromeInstaller) IsInstalled() bool {
	chromePath := ci.getChromeBinaryPath()
	if chromePath == "" {
		return false
	}
	_, err := os.Stat(chromePath)
	return err == nil
}

func (ci *ChromeInstaller) Install() error {
	if ci.IsInstalled() {
		return nil
	}

	fmt.Println("Downloading Chromium (this may take a few minutes)...")

	downloadURL, err := ci.getDownloadURL()
	if err != nil {
		return err
	}

	installPath := ci.getInstallPath()
	if err := os.MkdirAll(installPath, 0755); err != nil {
		return fmt.Errorf("failed to create install directory: %w", err)
	}

	zipPath := filepath.Join(installPath, "download.zip")
	defer os.Remove(zipPath)

	if err := ci.downloadFile(downloadURL, zipPath); err != nil {
		return fmt.Errorf("failed to download Chromium: %w", err)
	}

	if err := ci.extractZip(zipPath, installPath); err != nil {
		return fmt.Errorf("failed to extract Chromium: %w", err)
	}

	chromePath := ci.getChromeBinaryPath()
	if err := os.Chmod(chromePath, 0755); err != nil {
		return fmt.Errorf("failed to make Chrome executable: %w", err)
	}

	fmt.Println("Chromium installed successfully!")
	return nil
}

func (ci *ChromeInstaller) downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status: %s", resp.Status)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	return err
}

func (ci *ChromeInstaller) extractZip(zipPath, destPath string) error {
	return extractZipFile(zipPath, destPath)
}

func extractZipFile(zipPath, destPath string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip file: %w", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		filePath := filepath.Join(destPath, file.Name)

		if file.FileInfo().IsDir() {
			os.MkdirAll(filePath, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		dstFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return fmt.Errorf("failed to create file: %w", err)
		}
		defer dstFile.Close()

		srcFile, err := file.Open()
		if err != nil {
			return fmt.Errorf("failed to open zip entry: %w", err)
		}
		defer srcFile.Close()

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return fmt.Errorf("failed to extract file: %w", err)
		}
	}

	return nil
}

func AutoFindChromePath() string {
	path := FindChromePath()
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	installer := NewChromeInstaller()
	if installer.IsInstalled() {
		return installer.getChromeBinaryPath()
	}

	if err := installer.Install(); err == nil {
		return installer.getChromeBinaryPath()
	}

	return ""
}

func EnsureChrome() error {
	installer := NewChromeInstaller()
	if installer.IsInstalled() {
		return nil
	}

	return installer.Install()
}
