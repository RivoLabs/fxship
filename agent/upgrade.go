package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// upgrade downloads a release bundle (installer + binary), checks it against the release's
// checksums, unpacks it next to the earlier ones and hands over to its installer in upgrade mode
// (install.sh --upgrade, install.ps1 -Upgrade), which re-applies every step with the existing
// config and restarts the agent.
func upgrade(args []string) int {
	check, want := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			check = true
		case "--version":
			if i+1 < len(args) {
				want = args[i+1]
				i++
			}
		}
	}
	repo := env("UPGRADE_REPO", "RivoLabs/fxship")
	api := "https://api.github.com/repos/" + repo + "/releases/latest"
	if want != "" {
		api = "https://api.github.com/repos/" + repo + "/releases/tags/" + want
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := ghGet(api, "application/vnd.github+json", func(r io.Reader) error { return json.NewDecoder(r).Decode(&rel) }); err != nil {
		if strings.Contains(err.Error(), "404 Not Found") {
			if want != "" {
				fmt.Fprintf(os.Stderr, "%s has no release %s\n", repo, want)
			} else {
				fmt.Fprintf(os.Stderr, "%s has no release yet (pre-releases only count with --version)\n", repo)
			}
			return 1
		}
		fmt.Fprintln(os.Stderr, "checking for releases:", err)
		return 1
	}
	if want != "" {
		fmt.Printf("installed %s, upgrading to %s\n", version, rel.Tag)
	} else {
		fmt.Printf("installed %s, latest %s\n", version, rel.Tag)
	}
	if check {
		return 0
	}
	if rel.Tag == version && want == "" {
		fmt.Println("already up to date")
		return 0
	}
	if !isAdmin() {
		fmt.Fprintln(os.Stderr, "run it as root (sudo fxship upgrade), or from an elevated PowerShell on Windows")
		return 1
	}
	bundle := "fxship_" + strings.TrimPrefix(rel.Tag, "v") + "_linux_amd64.tar.gz"
	if runtime.GOOS == "windows" {
		bundle = "fxship_" + strings.TrimPrefix(rel.Tag, "v") + "_windows_amd64.zip"
	}
	assets := map[string]string{}
	for _, a := range rel.Assets {
		assets[a.Name] = a.URL
	}
	if assets[bundle] == "" || assets["checksums.txt"] == "" {
		fmt.Fprintf(os.Stderr, "release %s has no %s or checksums.txt\n", rel.Tag, bundle)
		return 1
	}
	dir := filepath.Join(paths().Releases, rel.Tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var sums string
	if err := ghGet(assets["checksums.txt"], "application/octet-stream", func(r io.Reader) error {
		b, err := io.ReadAll(io.LimitReader(r, 1<<20))
		sums = string(b)
		return err
	}); err != nil {
		fmt.Fprintln(os.Stderr, "downloading checksums:", err)
		return 1
	}
	tgz := filepath.Join(dir, bundle)
	h := sha256.New()
	if err := ghGet(assets[bundle], "application/octet-stream", func(r io.Reader) error {
		f, err := os.Create(tgz)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(io.MultiWriter(f, h), r)
		return err
	}); err != nil {
		fmt.Fprintln(os.Stderr, "downloading the release:", err)
		return 1
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.Contains(sums, got+"  "+bundle) {
		os.Remove(tgz)
		fmt.Fprintf(os.Stderr, "checksum mismatch for %s, nothing changed\n", bundle)
		return 1
	}
	fmt.Println("checksum ok, unpacking")
	// Windows 10+ and Server 2019+ ship bsdtar, which unpacks zip too.
	if out, err := exec.Command("tar", "-xf", tgz, "-C", dir, "--strip-components=1").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "unpacking: %v: %s\n", err, out)
		return 1
	}
	cmd := exec.Command("bash", filepath.Join(dir, "install.sh"), "--upgrade")
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(dir, "install.ps1"), "-Upgrade")
		cmd.Env = winPSEnv()
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "the installer failed:", err)
		return 1
	}
	return 0
}

// ghGet fetches a GitHub URL. GITHUB_TOKEN is used when set, for private repositories.
func ghGet(url, accept string, read func(io.Reader) error) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "fxship/"+version)
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s %s", url, resp.Status, strings.TrimSpace(string(b)))
	}
	return read(resp.Body)
}
