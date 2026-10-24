// M6 SPA 靜態服務測試：index.html、SPA fallback 與 API 路由優先級。
package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aliyun-guard-go/webapp"
)

// TestWebappServing 驗證 embed 的 SPA 服務：/ 回 index.html、未命中路徑
// fallback 回 index.html、_nuxt 資源可取得、/api 未登入仍 401。
func TestWebappServing(t *testing.T) {
	// dist 必須已建置（CI/本機先 pnpm generate + sync）
	if _, err := webapp.Dist.ReadFile("dist/index.html"); err != nil {
		t.Skipf("webapp/dist 未建置（先執行 pnpm generate）: %v", err)
	}
	paths := writeTestConfig(t, webTestConfig())
	server := &PanelServer{
		paths:    paths,
		sessions: newSessionStore(),
		limiter:  newLoginLimiter(),
		web:      getWebConfig(webTestConfig()),
		job:      newJob(),
	}
	ts := httptest.NewServer(server.routes())
	defer ts.Close()

	// / 回 index.html
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<div id=\"__nuxt\"") {
		t.Fatalf("GET / 應回 SPA index.html，得到 %d", resp.StatusCode)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("index.html 應 no-store，得到 %q", resp.Header.Get("Cache-Control"))
	}

	// SPA fallback：/instances 回 index.html
	resp, err = http.Get(ts.URL + "/instances")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<div id=\"__nuxt\"") {
		t.Fatalf("GET /instances 應 fallback 回 index.html，得到 %d", resp.StatusCode)
	}

	// _nuxt 資源取得（immutable 快取）
	matches, _ := webapp.Dist.ReadDir("dist/_nuxt")
	if len(matches) == 0 {
		t.Fatal("dist/_nuxt 應有資源")
	}
	asset := "/_nuxt/" + matches[0].Name()
	resp, err = http.Get(ts.URL + asset)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s 應 200，得到 %d", asset, resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("hashed 資源應 immutable 快取，得到 %q", resp.Header.Get("Cache-Control"))
	}

	// API 優先級不受 SPA 通配影響
	resp, err = http.Get(ts.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/dashboard 未登入應 401，得到 %d", resp.StatusCode)
	}
}
