// Package selfupdate upgrades a standalone dotpkg binary from a GitHub release.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const repository = "lukelex/dotpkg"
const maxBinary = 32 << 20
const maxChecksums = 64 << 10

type ReleaseUpdater interface {
	Check(context.Context, string, string) (string, bool, error)
	Install(context.Context, string, string) error
}

type Client struct {
	HTTP           *http.Client
	API, Downloads string
}

func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" || !allowedHost(req.URL.Hostname()) {
				return errors.New("refusing release redirect outside GitHub HTTPS")
			}
			return nil
		}},
		API: "https://api.github.com", Downloads: "https://github.com",
	}
}

func allowedHost(host string) bool {
	return host == "github.com" || host == "api.github.com" || host == "release-assets.githubusercontent.com"
}

func assetName() (string, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", fmt.Errorf("no standalone dotpkg release for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return "dotpkg-linux-" + runtime.GOARCH, nil
}

func versionParts(value string) ([3]uint64, error) {
	var result [3]uint64
	if !strings.HasPrefix(value, "v") {
		return result, fmt.Errorf("unknown dotpkg release version %q", value)
	}
	parts := strings.Split(value[1:], ".")
	if len(parts) != 3 {
		return result, fmt.Errorf("unknown dotpkg release version %q", value)
	}
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return result, fmt.Errorf("unknown dotpkg release version %q", value)
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, fmt.Errorf("unknown dotpkg release version %q: %w", value, err)
		}
		result[i] = n
	}
	return result, nil
}

func (c *Client) Check(ctx context.Context, executable, current string) (string, bool, error) {
	if _, err := assetName(); err != nil {
		return "", false, err
	}
	if err := standalone(ctx, executable); err != nil {
		return "", false, err
	}
	installed, err := versionParts(current)
	if err != nil {
		return "", false, err
	}
	data, err := c.fetch(ctx, c.API+"/repos/"+repository+"/releases/latest", maxChecksums)
	if err != nil {
		return "", false, err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(data, &release); err != nil {
		return "", false, fmt.Errorf("parse GitHub release: %w", err)
	}
	if release.Draft || release.Prerelease {
		return "", false, errors.New("latest GitHub release is not stable")
	}
	available, err := versionParts(release.Tag)
	if err != nil {
		return "", false, err
	}
	for i := range installed {
		if available[i] > installed[i] {
			return release.Tag, true, nil
		}
		if available[i] < installed[i] {
			return release.Tag, false, nil
		}
	}
	return release.Tag, false, nil
}

func standalone(ctx context.Context, executable string) error {
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("not a regular executable: %s", executable)
	}
	for _, manager := range []struct {
		command string
		args    []string
	}{
		{"pacman", []string{"-Qoq", "--", executable}},
		{"dpkg-query", []string{"-S", executable}},
		{"rpm", []string{"-qf", executable}},
	} {
		if _, err := exec.LookPath(manager.command); err != nil {
			continue
		}
		output, queryErr := exec.CommandContext(ctx, manager.command, manager.args...).Output()
		if queryErr == nil && strings.TrimSpace(string(output)) != "" {
			return fmt.Errorf("running executable is package-managed (%s); github self_update is only for standalone binaries", manager.command)
		}
	}
	return nil
}

func (c *Client) fetch(ctx context.Context, address string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download GitHub release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub release request failed: %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("GitHub release asset exceeds size limit")
	}
	return data, nil
}

func checksum(contents []byte, asset string) (string, error) {
	var digest string
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			if digest != "" || len(fields[0]) != 64 {
				return "", errors.New("invalid or duplicate release checksum")
			}
			if _, err := hex.DecodeString(fields[0]); err != nil {
				return "", fmt.Errorf("invalid release checksum: %w", err)
			}
			digest = strings.ToLower(fields[0])
		}
	}
	if digest == "" {
		return "", fmt.Errorf("release checksum missing for %s", asset)
	}
	return digest, nil
}

func (c *Client) Install(ctx context.Context, executable, tag string) error {
	asset, err := assetName()
	if err != nil {
		return err
	}
	if _, err := versionParts(tag); err != nil {
		return err
	}
	if err := standalone(ctx, executable); err != nil {
		return err
	}
	base := c.Downloads + "/" + repository + "/releases/download/" + tag + "/"
	sums, err := c.fetch(ctx, base+"SHA256SUMS", maxChecksums)
	if err != nil {
		return err
	}
	want, err := checksum(sums, asset)
	if err != nil {
		return err
	}
	binary, err := c.fetch(ctx, base+asset, maxBinary)
	if err != nil {
		return err
	}
	got := sha256.Sum256(binary)
	if hex.EncodeToString(got[:]) != want {
		return errors.New("release binary SHA256 mismatch; executable unchanged")
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular executable %s", executable)
	}
	file, err := os.CreateTemp(filepath.Dir(executable), ".dotpkg-update-*")
	if err != nil {
		return fmt.Errorf("create adjacent update file (check directory permissions): %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(binary); err != nil {
		return err
	}
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), executable); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	dir, err := os.Open(filepath.Dir(executable))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
