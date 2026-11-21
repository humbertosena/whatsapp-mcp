package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// sanitizeFilename / sanitizeChatDirName
// ---------------------------------------------------------------------------

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		want     string // exact result; ignored when fallback is true
		fallback bool   // expect document_<timestamp>
	}{
		{"path traversal", "../../etc/passwd", "passwd", false},
		{"absolute path", "/etc/passwd", "passwd", false},
		{"trailing slash", "reports/", "reports", false},
		{"double dot", "..", "", true},
		{"single dot", ".", "", true},
		{"empty", "", "", true},
		{"only spaces", "   ", "", true},
		{"only separators", "///", "", true},
		{"backslashes are removed", `..\..\evil.sh`, "....evil.sh", false},
		{"colon is removed", "C:report.txt", "Creport.txt", false},
		{"control characters are removed", "bad\x00na\nme\x7f.txt", "badname.txt", false},
		{"normal name with extension", "report 2024.pdf", "report 2024.pdf", false},
		{"non-ascii name", "relatório.pdf", "relatório.pdf", false},
		{"hidden file", ".env", ".env", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.in)
			if tt.fallback {
				if !strings.HasPrefix(got, "document_") {
					t.Fatalf("sanitizeFilename(%q) = %q, want document_<timestamp>", tt.in, got)
				}
			} else if got != tt.want {
				t.Fatalf("sanitizeFilename(%q) = %q, want %q", tt.in, got, tt.want)
			}
			assertSafeFilename(t, got)
		})
	}
}

func TestSanitizeFilenameLength(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantSuffix string
		wantLen    int // 0 means "any length up to the limit"
	}{
		{"long name keeps extension", strings.Repeat("a", 500) + ".pdf", ".pdf", 200},
		{"exactly at the limit is untouched", strings.Repeat("a", 196) + ".pdf", ".pdf", 200},
		{"2-byte characters", strings.Repeat("é", 300) + ".txt", ".txt", 0},
		{"3-byte characters are not split", strings.Repeat("€", 100) + ".txt", ".txt", 0},
		{"absurd extension is cut like a name", "a." + strings.Repeat("b", 300), "", 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.in)
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("result %q does not end with %q", got, tt.wantSuffix)
			}
			if tt.wantLen != 0 && len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(got), tt.wantLen)
			}
			assertSafeFilename(t, got)
		})
	}
}

// assertSafeFilename checks the invariants every sanitized name must satisfy.
func assertSafeFilename(t *testing.T, name string) {
	t.Helper()
	if name == "" || name == "." || name == ".." {
		t.Errorf("unsafe name %q", name)
	}
	if strings.ContainsAny(name, `/\:`) {
		t.Errorf("name %q contains a separator", name)
	}
	if filepath.Base(name) != name {
		t.Errorf("name %q is not a single path element", name)
	}
	if len(name) > maxFilenameBytes {
		t.Errorf("name is %d bytes, limit is %d", len(name), maxFilenameBytes)
	}
	if !utf8.ValidString(name) {
		t.Errorf("name %q is not valid UTF-8", name)
	}
}

func TestSanitizeChatDirName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"5511999999999@s.whatsapp.net", "5511999999999@s.whatsapp.net"},
		{"120363-1234@g.us", "120363-1234@g.us"},
		{"5511999999999:12@s.whatsapp.net", "5511999999999_12@s.whatsapp.net"},
		{"../../etc", ".._.._etc"},
		{"a/b\\c", "a_b_c"},
		{"grupo ç", "grupo__"},
		{"..", "_"},
		{".", "_"},
		{"", "_"},
	}
	for _, tt := range tests {
		if got := sanitizeChatDirName(tt.in); got != tt.want {
			t.Errorf("sanitizeChatDirName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := sanitizeChatDirName(strings.Repeat("a", 500)); len(got) != maxFilenameBytes {
		t.Errorf("long JID produced %d bytes, want %d", len(got), maxFilenameBytes)
	}
}

// ---------------------------------------------------------------------------
// media_path validation
// ---------------------------------------------------------------------------

// mediaFixture lays out:
//
//	root/media/ok.png, media/sub/inner.txt, media/..hidden
//	root/outside/secret.txt
//	root/media_evil/ok.png      (sibling sharing the "media" prefix)
func mediaFixture(t *testing.T) (root, media, outside string) {
	t.Helper()
	root = t.TempDir()
	media = filepath.Join(root, "media")
	outside = filepath.Join(root, "outside")
	for path, content := range map[string]string{
		filepath.Join(media, "ok.png"):              "png",
		filepath.Join(media, "sub", "inner.txt"):    "inner",
		filepath.Join(media, "..hidden"):            "hidden",
		filepath.Join(outside, "secret.txt"):        "secret",
		filepath.Join(root, "media_evil", "ok.png"): "evil",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root, media, outside
}

func TestResolveMediaPath(t *testing.T) {
	root, media, outside := mediaFixture(t)
	realMedia, err := filepath.EvalSymlinks(media)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantRel string // path relative to the media dir when allowed; "" when it must be rejected
	}{
		{"file inside", filepath.Join(media, "ok.png"), "ok.png"},
		{"file in subdirectory", filepath.Join(media, "sub", "inner.txt"), filepath.Join("sub", "inner.txt")},
		{"dot-dot that stays inside", media + "/sub/../ok.png", "ok.png"},
		{"name starting with two dots is not traversal", filepath.Join(media, "..hidden"), "..hidden"},
		{"file outside", filepath.Join(outside, "secret.txt"), ""},
		{"dot-dot escape", media + "/../outside/secret.txt", ""},
		{"deep dot-dot escape", media + "/sub/../../outside/secret.txt", ""},
		{"sibling with the same prefix", filepath.Join(root, "media_evil", "ok.png"), ""},
		{"system file", "/etc/passwd", ""},
		{"directory", filepath.Join(media, "sub"), ""},
		{"the media directory itself", media, ""},
		{"missing file inside", filepath.Join(media, "nope.png"), ""},
		{"empty path", "", ""},
		{"relative path", "ok.png", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMediaPath(media, tt.path)
			if tt.wantRel == "" {
				if err == nil {
					t.Fatalf("resolveMediaPath(%q) = %q, want an error", tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMediaPath(%q) failed: %v", tt.path, err)
			}
			if want := filepath.Join(realMedia, tt.wantRel); got != want {
				t.Fatalf("resolveMediaPath(%q) = %q, want %q", tt.path, got, want)
			}
		})
	}
}

func TestResolveMediaPathSymlinks(t *testing.T) {
	_, media, outside := mediaFixture(t)
	links := map[string]string{
		"link_in.png":   filepath.Join(media, "ok.png"),
		"link_out.txt":  filepath.Join(outside, "secret.txt"),
		"dir_link":      outside,
		"link_to_dir":   filepath.Join(media, "sub"),
		"dangling.link": filepath.Join(media, "does-not-exist"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(media, name)); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
	}
	realMedia, err := filepath.EvalSymlinks(media)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("symlink to a file inside resolves to the target", func(t *testing.T) {
		got, err := resolveMediaPath(media, filepath.Join(media, "link_in.png"))
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(realMedia, "ok.png"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	for _, name := range []string{"link_out.txt", "link_to_dir", "dangling.link"} {
		t.Run("rejected "+name, func(t *testing.T) {
			if got, err := resolveMediaPath(media, filepath.Join(media, name)); err == nil {
				t.Fatalf("expected an error, got %q", got)
			}
		})
	}
	t.Run("file reached through a symlinked directory pointing outside", func(t *testing.T) {
		if got, err := resolveMediaPath(media, filepath.Join(media, "dir_link", "secret.txt")); err == nil {
			t.Fatalf("expected an error, got %q", got)
		}
	})
	t.Run("media dir given as a symlink", func(t *testing.T) {
		linkToMedia := filepath.Join(t.TempDir(), "media_link")
		if err := os.Symlink(media, linkToMedia); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
		if _, err := resolveMediaPath(linkToMedia, filepath.Join(media, "ok.png")); err != nil {
			t.Fatalf("a symlinked media dir must still work: %v", err)
		}
		if _, err := resolveMediaPath(linkToMedia, filepath.Join(outside, "secret.txt")); err == nil {
			t.Fatal("file outside must be rejected")
		}
	})
}

func TestIsWithinDir(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "data", "store")
	tests := []struct {
		target string
		want   bool
	}{
		{filepath.Join(base, "chat", "file.jpg"), true},
		{filepath.Join(base, "file.jpg"), true},
		{filepath.Join(base, "..hidden"), true},
		{base, false},
		{filepath.Join(base, "..", "other"), false},
		{base + "_evil", false},
		{filepath.Join(string(filepath.Separator), "etc", "passwd"), false},
	}
	for _, tt := range tests {
		if got := isWithinDir(base, tt.target); got != tt.want {
			t.Errorf("isWithinDir(%q, %q) = %v, want %v", base, tt.target, got, tt.want)
		}
	}
}

func TestSetupMediaDirCreatesPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "media")
	t.Setenv("WHATSAPP_MEDIA_DIR", dir)
	got, err := setupMediaDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("got %q, want %q", got, dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("media dir was not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Errorf("mode = %v, want 0700", info.Mode().Perm())
	}
}

// ---------------------------------------------------------------------------
// authentication and CSRF middleware
// ---------------------------------------------------------------------------

const testToken = "0123456789abcdef0123456789abcdef"

func TestAPIGuard(t *testing.T) {
	bearer := "Bearer " + testToken
	tests := []struct {
		name       string
		serverTok  string // defaults to testToken
		header     http.Header
		wantStatus int
	}{
		{"valid request", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json"}}, http.StatusOK},
		{"valid with charset", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json; charset=utf-8"}}, http.StatusOK},
		{"valid with Sec-Fetch-Site none", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json"}, "Sec-Fetch-Site": {"none"}}, http.StatusOK},

		{"no token", "", http.Header{"Content-Type": {"application/json"}}, http.StatusUnauthorized},
		{"wrong token", "", http.Header{"Authorization": {"Bearer wrong"}, "Content-Type": {"application/json"}}, http.StatusUnauthorized},
		{"token with one wrong character", "", http.Header{"Authorization": {"Bearer " + testToken[:31] + "x"}, "Content-Type": {"application/json"}}, http.StatusUnauthorized},
		{"token without Bearer scheme", "", http.Header{"Authorization": {testToken}, "Content-Type": {"application/json"}}, http.StatusUnauthorized},
		{"other auth scheme", "", http.Header{"Authorization": {"Basic " + testToken}, "Content-Type": {"application/json"}}, http.StatusUnauthorized},
		{"empty server token never authenticates", "-", http.Header{"Authorization": {"Bearer "}, "Content-Type": {"application/json"}}, http.StatusUnauthorized},

		{"Origin present", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json"}, "Origin": {"https://evil.example"}}, http.StatusForbidden},
		{"Origin present but empty", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json"}, "Origin": {""}}, http.StatusForbidden},
		{"Origin without token", "", http.Header{"Content-Type": {"application/json"}, "Origin": {"https://evil.example"}}, http.StatusForbidden},
		{"Sec-Fetch-Site cross-site", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json"}, "Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		{"Sec-Fetch-Site same-origin", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json"}, "Sec-Fetch-Site": {"same-origin"}}, http.StatusForbidden},
		{"Sec-Fetch-Site empty", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/json"}, "Sec-Fetch-Site": {""}}, http.StatusForbidden},

		{"wrong content type", "", http.Header{"Authorization": {bearer}, "Content-Type": {"text/plain"}}, http.StatusUnsupportedMediaType},
		{"form content type (simple CSRF request)", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/x-www-form-urlencoded"}}, http.StatusUnsupportedMediaType},
		{"missing content type", "", http.Header{"Authorization": {bearer}}, http.StatusUnsupportedMediaType},
		{"look-alike content type", "", http.Header{"Authorization": {bearer}, "Content-Type": {"application/jsonx"}}, http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := testToken
			switch tt.serverTok {
			case "":
			case "-":
				token = ""
			default:
				token = tt.serverTok
			}

			called := false
			handler := apiGuard(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader("{}"))
			req.Header = tt.header
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if wantCalled := tt.wantStatus == http.StatusOK; called != wantCalled {
				t.Errorf("handler called = %v, want %v", called, wantCalled)
			}
		})
	}
}

// Browsers send header names in lower case over HTTP/2 and some proxies do too;
// net/http canonicalizes them, which the guard relies on.
func TestAPIGuardRejectsBrowserHeadersOverTheWire(t *testing.T) {
	srv := httptest.NewServer(limitRequestBody(apiGuard(testToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))))
	defer srv.Close()

	send := func(extraHeader string) int {
		t.Helper()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		raw := "POST /api/send HTTP/1.1\r\nHost: localhost\r\nauthorization: Bearer " + testToken +
			"\r\ncontent-type: application/json\r\ncontent-length: 2\r\nconnection: close\r\n" + extraHeader + "\r\n{}"
		if _, err := io.WriteString(conn, raw); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := send(""); got != http.StatusOK {
		t.Errorf("plain client: status = %d, want 200", got)
	}
	if got := send("origin: https://evil.example\r\n"); got != http.StatusForbidden {
		t.Errorf("lower-case origin: status = %d, want 403", got)
	}
	if got := send("sec-fetch-site: cross-site\r\n"); got != http.StatusForbidden {
		t.Errorf("lower-case sec-fetch-site: status = %d, want 403", got)
	}
}

func TestLimitRequestBody(t *testing.T) {
	handler := limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			writeBodyError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	for _, tt := range []struct {
		name string
		size int
		want int
	}{
		{"small body", 10, http.StatusOK},
		{"exactly at the limit", maxRequestBodyBytes, http.StatusOK},
		{"one byte over", maxRequestBodyBytes + 1, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(strings.Repeat("a", tt.size)))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestBridgeAddrDefaultsToLoopback(t *testing.T) {
	t.Setenv("WHATSAPP_BRIDGE_ADDR", "")
	if got := bridgeAddr(); got != "127.0.0.1:8080" {
		t.Errorf("default = %q, want 127.0.0.1:8080", got)
	}
	t.Setenv("WHATSAPP_BRIDGE_ADDR", "127.0.0.1:9999")
	if got := bridgeAddr(); got != "127.0.0.1:9999" {
		t.Errorf("override = %q, want 127.0.0.1:9999", got)
	}
}

// ---------------------------------------------------------------------------
// token, permissions and logging
// ---------------------------------------------------------------------------

func TestLoadOrCreateToken(t *testing.T) {
	t.Run("environment variable wins", func(t *testing.T) {
		t.Setenv("WHATSAPP_BRIDGE_TOKEN", "  "+testToken+"  ")
		dir := t.TempDir()
		got, err := loadOrCreateToken(dir)
		if err != nil || got != testToken {
			t.Fatalf("got %q, %v", got, err)
		}
		if _, err := os.Stat(filepath.Join(dir, tokenFileName)); err == nil {
			t.Error("no file should be written when the env var is set")
		}
	})

	t.Run("too short env token is refused", func(t *testing.T) {
		t.Setenv("WHATSAPP_BRIDGE_TOKEN", "short")
		if _, err := loadOrCreateToken(t.TempDir()); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("generated once, then reused", func(t *testing.T) {
		t.Setenv("WHATSAPP_BRIDGE_TOKEN", "")
		dir := t.TempDir()
		first, err := loadOrCreateToken(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(first) != 64 || strings.Trim(first, "0123456789abcdef") != "" {
			t.Fatalf("token %q is not 32 bytes of hex", first)
		}
		assertMode(t, filepath.Join(dir, tokenFileName), 0600)

		second, err := loadOrCreateToken(dir)
		if err != nil || second != first {
			t.Fatalf("second call = %q, %v; want the same token %q", second, err, first)
		}

		other, err := loadOrCreateToken(t.TempDir())
		if err != nil || other == first {
			t.Fatalf("tokens must be random; got %q and %q (%v)", first, other, err)
		}
	})

	t.Run("empty file is replaced and made private", func(t *testing.T) {
		t.Setenv("WHATSAPP_BRIDGE_TOKEN", "")
		dir := t.TempDir()
		path := filepath.Join(dir, tokenFileName)
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		got, err := loadOrCreateToken(dir)
		if err != nil || len(got) != 64 {
			t.Fatalf("got %q, %v", got, err)
		}
		assertMode(t, path, 0600)
	})
}

func TestHardenStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions only")
	}
	root := t.TempDir()
	store := filepath.Join(root, "store")
	chatDir := filepath.Join(store, "5511@s.whatsapp.net")
	for _, d := range []string{store, chatDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{
		filepath.Join(store, "whatsapp.db"),
		filepath.Join(store, "messages.db"),
		filepath.Join(store, tokenFileName),
		filepath.Join(chatDir, "photo.jpg"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(f, 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink must not make us chmod something outside the store.
	outsideFile := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outsideFile, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(store, "link")); err != nil {
		t.Fatal(err)
	}

	if err := hardenStorePermissions(store); err != nil {
		t.Fatal(err)
	}

	assertMode(t, store, 0700)
	assertMode(t, chatDir, 0700)
	for _, f := range files {
		assertMode(t, f, 0600)
	}
	assertMode(t, outsideFile, 0644)

	// Running it on a missing directory reports the problem instead of panicking.
	if err := hardenStorePermissions(filepath.Join(root, "missing")); err != nil {
		t.Logf("missing dir reported: %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode = %o, want %o", filepath.Base(path), got, want)
	}
}

func TestMessageLogLineHidesContentByDefault(t *testing.T) {
	old := logContent
	t.Cleanup(func() { logContent = old })

	const (
		sender   = "5511987654321"
		filename = "contract-acme.pdf"
		content  = "my card number is 4111"
	)

	logContent = false
	for _, line := range []string{
		messageLogLine(true, sender, "", "", content, 0),
		messageLogLine(false, sender, "document", filename, content, 2048),
	} {
		for _, secret := range []string{sender, filename, content, "4111"} {
			if strings.Contains(line, secret) {
				t.Errorf("log line %q leaks %q", line, secret)
			}
		}
	}
	if got, want := messageLogLine(true, sender, "", "", content, 0), "message out type=text size=22"; got != want {
		t.Errorf("text line = %q, want %q", got, want)
	}
	if got, want := messageLogLine(false, sender, "document", filename, content, 2048), "message in type=document size=2048"; got != want {
		t.Errorf("media line = %q, want %q", got, want)
	}

	logContent = true
	line := messageLogLine(false, sender, "document", filename, content, 2048)
	for _, want := range []string{sender, filename, content} {
		if !strings.Contains(line, want) {
			t.Errorf("verbose log line %q is missing %q", line, want)
		}
	}
}
