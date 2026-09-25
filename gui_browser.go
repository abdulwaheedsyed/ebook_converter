package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// openUI shows the interface. A Chromium-based browser (Chrome, Edge,
// Chromium, Brave) can show it as an app window, without tabs or an address
// bar, so one is used when installed; otherwise the default browser opens it.
//
// LEAFBIND_BROWSER overrides the choice: "default" for the default
// browser, or the path of a Chromium-based browser.
func openUI(url string) error {
	pref := os.Getenv("LEAFBIND_BROWSER")
	if pref != "default" {
		cands := appBrowsers()
		if pref != "" {
			cands = []string{pref}
		}
		for _, b := range cands {
			cmd := exec.Command(b, "--app="+url, "--new-window", "--window-size=1240,860")
			if cmd.Start() == nil {
				go cmd.Wait()
				return nil
			}
		}
	}
	return openDefault(url)
}

// appBrowsers lists installed Chromium-based browsers, most likely first.
func appBrowsers() []string {
	var paths []string
	switch runtime.GOOS {
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
			if root == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`),
				filepath.Join(root, `Chromium\Application\chrome.exe`),
			)
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			paths = append(paths,
				filepath.Join(root, "Google Chrome.app/Contents/MacOS/Google Chrome"),
				filepath.Join(root, "Microsoft Edge.app/Contents/MacOS/Microsoft Edge"),
				filepath.Join(root, "Chromium.app/Contents/MacOS/Chromium"),
				filepath.Join(root, "Brave Browser.app/Contents/MacOS/Brave Browser"),
			)
		}
	default:
		for _, name := range []string{
			"google-chrome-stable", "google-chrome", "chromium", "chromium-browser",
			"microsoft-edge-stable", "microsoft-edge", "brave-browser", "brave",
		} {
			if p, err := exec.LookPath(name); err == nil {
				paths = append(paths, p)
			}
		}
	}
	var found []string
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			found = append(found, p)
		}
	}
	return found
}

func openDefault(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return errors.New("no browser found")
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
