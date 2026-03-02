package browser

import (
	"os"
	"path/filepath"
	"runtime"
)

func DetectPlatform() string {
	return runtime.GOOS
}

func GetDefaultChromePath() string {
	switch runtime.GOOS {
	case "linux":
		return "/usr/bin/chromium"
	case "windows":
		return "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe"
	case "darwin":
		return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	default:
		return ""
	}
}

func GetChromeSearchPaths() []string {
	switch runtime.GOOS {
	case "linux":
		return []string{
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/snap/bin/chromium",
			"/opt/google/chrome/chrome",
		}
	case "windows":
		programFiles := os.Getenv("ProgramFiles")
		programFilesX86 := os.Getenv("ProgramFiles(x86)")
		localAppData := os.Getenv("LOCALAPPDATA")

		paths := []string{}
		if programFiles != "" {
			paths = append(paths, filepath.Join(programFiles, "Google", "Chrome", "Application", "chrome.exe"))
			paths = append(paths, filepath.Join(programFiles, "Chromium", "Application", "chrome.exe"))
		}
		if programFilesX86 != "" {
			paths = append(paths, filepath.Join(programFilesX86, "Google", "Chrome", "Application", "chrome.exe"))
			paths = append(paths, filepath.Join(programFilesX86, "Chromium", "Application", "chrome.exe"))
		}
		if localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"))
			paths = append(paths, filepath.Join(localAppData, "Chromium", "Application", "chrome.exe"))
		}
		return paths
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome Beta.app/Contents/MacOS/Google Chrome Beta",
		}
	default:
		return []string{}
	}
}

func FindChromePath() string {
	if chromePath := os.Getenv("CHROME_PATH"); chromePath != "" {
		if _, err := os.Stat(chromePath); err == nil {
			return chromePath
		}
	}

	for _, path := range GetChromeSearchPaths() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return GetDefaultChromePath()
}

func ChromeAvailable() bool {
	path := FindChromePath()
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}

	installer := NewChromeInstaller()
	return installer.IsInstalled()
}
