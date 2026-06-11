package cli

// update.go implements `wisdev update`: self-update the CLI binary from the
// latest GitHub release. Mirrors the conventions of scripts/install.sh and
// .github/workflows/release.yml — assets are named
// wisdev_<tag>_<goos>_<goarch>.tar.gz (.zip on windows) and a SHA256SUMS file
// accompanies every release, which we verify before replacing the binary.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultUpdateRepo = "bharathvbcr/WisDev"

type updateOptions struct {
	repo         string // GitHub owner/name hosting releases
	version      string // explicit tag to install; "" means latest
	checkOnly    bool
	force        bool
	timeout      time.Duration
	apiBase      string // https://api.github.com; test override
	downloadBase string // https://github.com; test override
	goos         string
	goarch       string
	execPath     func() (string, error)
}

func runUpdate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	check := fs.Bool("check", false, "only check whether an update is available")
	version := fs.String("version", "", "install a specific release tag (default: latest)")
	force := fs.Bool("force", false, "replace the binary even from a dev build or for a downgrade")
	timeout := fs.Duration("timeout", 2*time.Minute, "download timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	opts := updateOptions{
		repo:         envOrDefault("WISDEV_REPO", defaultUpdateRepo),
		version:      strings.TrimSpace(*version),
		checkOnly:    *check,
		force:        *force,
		timeout:      *timeout,
		apiBase:      "https://api.github.com",
		downloadBase: "https://github.com",
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		execPath:     os.Executable,
	}
	return performUpdate(opts, stdout, stderr)
}

func performUpdate(opts updateOptions, stdout, stderr io.Writer) error {
	client := &http.Client{Timeout: opts.timeout}

	tag := normalizeReleaseTag(opts.version)
	if tag == "" {
		note(stderr, "Checking %s for the latest release...", opts.repo)
		latest, err := resolveLatestReleaseTag(client, opts.apiBase, opts.repo)
		if err != nil {
			return fmt.Errorf("resolve latest release: %w", err)
		}
		tag = latest
	}

	cmp, comparable := compareReleaseVersions(Version, tag)
	switch {
	case comparable && cmp == 0:
		fmt.Fprintf(stdout, "wisdev %s is already the latest release.\n", Version)
		return nil
	case comparable && cmp > 0 && !opts.force:
		return fmt.Errorf("current version %s is newer than %s; pass --force to downgrade", Version, tag)
	}

	if opts.checkOnly {
		fmt.Fprintf(stdout, "Update available: %s -> %s\n", Version, tag)
		fmt.Fprintf(stdout, "Run: wisdev update\n")
		return nil
	}
	if !comparable && !opts.force {
		// Typically a source build (`Version == "dev"`); don't silently clobber
		// a developer's locally compiled binary with a release download.
		return fmt.Errorf("current build is %q (not a release); pass --force to replace it with %s", Version, tag)
	}

	target, err := opts.execPath()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil && resolved != "" {
		target = resolved
	}
	cleanupOldBinary(target)

	asset := updateAssetName(tag, opts.goos, opts.goarch)
	note(stderr, "Downloading %s...", asset)
	archive, err := downloadReleaseFile(client, opts.downloadBase, opts.repo, tag, asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	sums, err := downloadReleaseFile(client, opts.downloadBase, opts.repo, tag, "SHA256SUMS")
	if err != nil {
		return fmt.Errorf("download SHA256SUMS: %w", err)
	}
	if err := verifyUpdateChecksum(archive, string(sums), asset); err != nil {
		return err
	}
	note(stderr, "Checksum verified.")

	binary, err := extractWisdevBinary(archive, asset)
	if err != nil {
		return fmt.Errorf("extract binary from %s: %w", asset, err)
	}

	if err := installUpdatedBinary(binary, target); err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	fmt.Fprintf(stdout, "Updated wisdev %s -> %s\n", Version, tag)
	fmt.Fprintf(stdout, "Binary: %s\n", target)
	return nil
}

// normalizeReleaseTag adds the leading "v" release tags carry (the release
// workflow tags v* and embeds the tag verbatim in asset names).
func normalizeReleaseTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if !strings.HasPrefix(tag, "v") {
		return "v" + tag
	}
	return tag
}

func resolveLatestReleaseTag(client *http.Client, apiBase, repo string) (string, error) {
	url := strings.TrimRight(apiBase, "/") + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// api.github.com rejects requests without a User-Agent.
	req.Header.Set("User-Agent", "wisdev-cli/"+Version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return "", fmt.Errorf("parse release response: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", fmt.Errorf("release response from %s had no tag_name", url)
	}
	return release.TagName, nil
}

func downloadReleaseFile(client *http.Client, downloadBase, repo, tag, name string) ([]byte, error) {
	url := strings.TrimRight(downloadBase, "/") + "/" + repo + "/releases/download/" + tag + "/" + name
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "wisdev-cli/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// updateAssetName mirrors the naming in .github/workflows/release.yml.
func updateAssetName(tag, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("wisdev_%s_%s_%s.%s", tag, goos, goarch, ext)
}

// compareReleaseVersions compares two release labels numerically. ok is false
// when either side is not a vMAJOR.MINOR[.PATCH] label (e.g. a "dev" source
// build), in which case cmp is meaningless.
func compareReleaseVersions(current, latest string) (cmp int, ok bool) {
	a, okA := parseReleaseVersion(current)
	b, okB := parseReleaseVersion(latest)
	if !okA || !okB {
		return 0, false
	}
	for i := range 3 {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseReleaseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// verifyUpdateChecksum checks the downloaded archive against the release's
// SHA256SUMS file (sha256sum format: "<hex><space><space-or-*><name>").
func verifyUpdateChecksum(archive []byte, sums, assetName string) error {
	want := ""
	for line := range strings.SplitSeq(sums, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == assetName {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", assetName)
	}
	sum := sha256.Sum256(archive)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, got, want)
	}
	return nil
}

// extractWisdevBinary pulls the wisdev executable out of a release archive
// (tar.gz with "wisdev" on unix, zip with "wisdev.exe" on windows).
func extractWisdevBinary(archive []byte, assetName string) ([]byte, error) {
	if strings.HasSuffix(assetName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) != "wisdev.exe" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 256<<20))
		}
		return nil, fmt.Errorf("wisdev.exe not found in archive")
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "wisdev" {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, 256<<20))
	}
	return nil, fmt.Errorf("wisdev binary not found in archive")
}

// installUpdatedBinary swaps the new binary into place with the portable
// rename dance: write next to the target, move the running binary aside, move
// the new one in. Windows allows renaming a running executable but not
// overwriting or deleting it, so a failed removal of the old file is left for
// cleanupOldBinary on the next update.
func installUpdatedBinary(binary []byte, target string) error {
	newPath := target + ".new"
	if err := os.WriteFile(newPath, binary, 0o755); err != nil {
		return err
	}
	oldPath := target + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(target, oldPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := os.Rename(newPath, target); err != nil {
		// Try to roll back so the user still has a working binary.
		_ = os.Rename(oldPath, target)
		_ = os.Remove(newPath)
		return fmt.Errorf("move new binary into place: %w", err)
	}
	// Best effort: fails on Windows while the old binary is still running.
	_ = os.Remove(oldPath)
	return nil
}

// cleanupOldBinary removes the .old file a previous Windows update could not
// delete while it was still the running process.
func cleanupOldBinary(target string) {
	_ = os.Remove(target + ".old")
	_ = os.Remove(target + ".new")
}
