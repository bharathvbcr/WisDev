package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateAssetName(t *testing.T) {
	if got := updateAssetName("v0.1.3", "darwin", "arm64"); got != "wisdev_v0.1.3_darwin_arm64.tar.gz" {
		t.Fatalf("darwin asset = %q", got)
	}
	if got := updateAssetName("v0.1.3", "windows", "amd64"); got != "wisdev_v0.1.3_windows_amd64.zip" {
		t.Fatalf("windows asset = %q", got)
	}
}

func TestNormalizeReleaseTag(t *testing.T) {
	if got := normalizeReleaseTag("0.1.3"); got != "v0.1.3" {
		t.Fatalf("missing v prefix not added: %q", got)
	}
	if got := normalizeReleaseTag("v0.1.3"); got != "v0.1.3" {
		t.Fatalf("v prefix mangled: %q", got)
	}
	if got := normalizeReleaseTag("  "); got != "" {
		t.Fatalf("blank tag should stay empty: %q", got)
	}
}

func TestCompareReleaseVersions(t *testing.T) {
	cases := []struct {
		current, latest string
		cmp             int
		ok              bool
	}{
		{"v0.1.2", "v0.1.3", -1, true},
		{"v0.1.3", "v0.1.3", 0, true},
		{"v0.2.0", "v0.1.9", 1, true},
		{"0.1.3", "v0.1.3", 0, true},
		{"v1.0", "v1.0.0", 0, true},
		{"v0.1.3-rc1", "v0.1.3", 0, true}, // prerelease suffix ignored
		{"dev", "v0.1.3", 0, false},
		{"", "v0.1.3", 0, false},
	}
	for _, c := range cases {
		cmp, ok := compareReleaseVersions(c.current, c.latest)
		if cmp != c.cmp || ok != c.ok {
			t.Errorf("compareReleaseVersions(%q, %q) = (%d, %v), want (%d, %v)",
				c.current, c.latest, cmp, ok, c.cmp, c.ok)
		}
	}
}

func TestVerifyUpdateChecksum(t *testing.T) {
	archive := []byte("release archive bytes")
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	sums := fmt.Sprintf("%s  other.tar.gz\n%s  wisdev_v1_linux_amd64.tar.gz\n", strings.Repeat("0", 64), hexSum)

	if err := verifyUpdateChecksum(archive, sums, "wisdev_v1_linux_amd64.tar.gz"); err != nil {
		t.Fatalf("valid checksum rejected: %v", err)
	}
	if err := verifyUpdateChecksum([]byte("tampered"), sums, "wisdev_v1_linux_amd64.tar.gz"); err == nil {
		t.Fatal("tampered archive accepted")
	}
	if err := verifyUpdateChecksum(archive, sums, "missing.tar.gz"); err == nil {
		t.Fatal("missing SHA256SUMS entry accepted")
	}
	// sha256sum binary-mode marker (*name) must also match.
	binMode := hexSum + " *wisdev_v1_linux_amd64.tar.gz\n"
	if err := verifyUpdateChecksum(archive, binMode, "wisdev_v1_linux_amd64.tar.gz"); err != nil {
		t.Fatalf("binary-mode sums line rejected: %v", err)
	}
}

func makeTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractWisdevBinary(t *testing.T) {
	want := []byte("unix binary")
	got, err := extractWisdevBinary(makeTarGz(t, "wisdev", want), "wisdev_v1_linux_amd64.tar.gz")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("tar.gz extract = (%q, %v), want %q", got, err, want)
	}

	want = []byte("windows binary")
	got, err = extractWisdevBinary(makeZip(t, "wisdev.exe", want), "wisdev_v1_windows_amd64.zip")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("zip extract = (%q, %v), want %q", got, err, want)
	}

	if _, err := extractWisdevBinary(makeTarGz(t, "README.md", []byte("x")), "wisdev_v1_linux_amd64.tar.gz"); err == nil {
		t.Fatal("archive without wisdev binary accepted")
	}
}

func TestInstallUpdatedBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wisdev")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installUpdatedBinary([]byte("new"), target); err != nil {
		t.Fatalf("installUpdatedBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new" {
		t.Fatalf("target after install = (%q, %v), want \"new\"", got, err)
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Fatalf(".new staging file left behind: %v", err)
	}
}

// fakeReleaseServer serves the GitHub API latest-release endpoint and release
// asset downloads from one httptest server.
func fakeReleaseServer(t *testing.T, repo, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "missing user agent", http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"tag_name": %q}`, tag)
	})
	mux.HandleFunc("/"+repo+"/releases/download/"+tag+"/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		body, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	return httptest.NewServer(mux)
}

func TestPerformUpdateEndToEnd(t *testing.T) {
	oldVersion := Version
	Version = "v0.1.0"
	defer func() { Version = oldVersion }()

	const repo = "example/WisDev"
	const tag = "v0.2.0"
	binary := []byte("#!/fake new wisdev binary")
	asset := updateAssetName(tag, "linux", "amd64")
	archive := makeTarGz(t, "wisdev", binary)
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"

	srv := fakeReleaseServer(t, repo, tag, map[string][]byte{
		asset:        archive,
		"SHA256SUMS": []byte(sums),
	})
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "wisdev")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := updateOptions{
		repo:         repo,
		timeout:      5 * time.Second,
		apiBase:      srv.URL,
		downloadBase: srv.URL,
		goos:         "linux",
		goarch:       "amd64",
		execPath:     func() (string, error) { return target, nil },
	}

	var stdout, stderr bytes.Buffer
	if err := performUpdate(opts, &stdout, &stderr); err != nil {
		t.Fatalf("performUpdate: %v\nstderr: %s", err, stderr.String())
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("binary not replaced: (%q, %v)", got, err)
	}
	if !strings.Contains(stdout.String(), "Updated wisdev v0.1.0 -> v0.2.0") {
		t.Fatalf("missing success message: %q", stdout.String())
	}
}

func TestPerformUpdateCheckOnly(t *testing.T) {
	oldVersion := Version
	Version = "v0.1.0"
	defer func() { Version = oldVersion }()

	const repo = "example/WisDev"
	srv := fakeReleaseServer(t, repo, "v0.2.0", nil)
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "wisdev")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := updateOptions{
		repo:         repo,
		checkOnly:    true,
		timeout:      5 * time.Second,
		apiBase:      srv.URL,
		downloadBase: srv.URL,
		goos:         "linux",
		goarch:       "amd64",
		execPath:     func() (string, error) { return target, nil },
	}
	var stdout, stderr bytes.Buffer
	if err := performUpdate(opts, &stdout, &stderr); err != nil {
		t.Fatalf("performUpdate --check: %v", err)
	}
	if !strings.Contains(stdout.String(), "Update available: v0.1.0 -> v0.2.0") {
		t.Fatalf("missing availability message: %q", stdout.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "old binary" {
		t.Fatalf("--check must not touch the binary, got %q", got)
	}
}

func TestPerformUpdateAlreadyLatest(t *testing.T) {
	oldVersion := Version
	Version = "v0.2.0"
	defer func() { Version = oldVersion }()

	const repo = "example/WisDev"
	srv := fakeReleaseServer(t, repo, "v0.2.0", nil)
	defer srv.Close()

	opts := updateOptions{
		repo:         repo,
		timeout:      5 * time.Second,
		apiBase:      srv.URL,
		downloadBase: srv.URL,
		goos:         "linux",
		goarch:       "amd64",
		execPath:     func() (string, error) { return "", fmt.Errorf("must not be called") },
	}
	var stdout, stderr bytes.Buffer
	if err := performUpdate(opts, &stdout, &stderr); err != nil {
		t.Fatalf("performUpdate at latest: %v", err)
	}
	if !strings.Contains(stdout.String(), "already the latest release") {
		t.Fatalf("missing up-to-date message: %q", stdout.String())
	}
}

func TestPerformUpdateDevBuildNeedsForce(t *testing.T) {
	oldVersion := Version
	Version = "dev"
	defer func() { Version = oldVersion }()

	const repo = "example/WisDev"
	srv := fakeReleaseServer(t, repo, "v0.2.0", nil)
	defer srv.Close()

	opts := updateOptions{
		repo:         repo,
		timeout:      5 * time.Second,
		apiBase:      srv.URL,
		downloadBase: srv.URL,
		goos:         "linux",
		goarch:       "amd64",
		execPath:     func() (string, error) { return "", fmt.Errorf("must not be called") },
	}
	var stdout, stderr bytes.Buffer
	err := performUpdate(opts, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("dev build without --force should error with guidance, got %v", err)
	}
}

func TestPerformUpdateChecksumMismatchAborts(t *testing.T) {
	oldVersion := Version
	Version = "v0.1.0"
	defer func() { Version = oldVersion }()

	const repo = "example/WisDev"
	const tag = "v0.2.0"
	asset := updateAssetName(tag, "linux", "amd64")
	archive := makeTarGz(t, "wisdev", []byte("new binary"))
	wrongSums := strings.Repeat("0", 64) + "  " + asset + "\n"

	srv := fakeReleaseServer(t, repo, tag, map[string][]byte{
		asset:        archive,
		"SHA256SUMS": []byte(wrongSums),
	})
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "wisdev")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := updateOptions{
		repo:         repo,
		timeout:      5 * time.Second,
		apiBase:      srv.URL,
		downloadBase: srv.URL,
		goos:         "linux",
		goarch:       "amd64",
		execPath:     func() (string, error) { return target, nil },
	}
	var stdout, stderr bytes.Buffer
	err := performUpdate(opts, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old binary" {
		t.Fatalf("binary must be untouched on checksum failure, got %q", got)
	}
}

func TestUpdateCommandWiring(t *testing.T) {
	if !isKnownCommand("update") || !isKnownCommand("upgrade") {
		t.Fatal("update/upgrade should be known commands")
	}
	args := normalizeInvocation([]string{"upgrade", "--check"})
	if args[0] != "update" {
		t.Fatalf("upgrade alias not normalized: %v", args)
	}
	if _, ok := commandHelp["update"]; !ok {
		t.Fatal("missing help topic for update")
	}
}
