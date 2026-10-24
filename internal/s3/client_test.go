package s3

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSignAWSExample 採用 AWS SigV4 官方文件中 S3 GET Object 範例作為
// golden 測試（examplebucket / 20130524 / us-east-1）。
func TestSignAWSExample(t *testing.T) {
	in := signInput{
		method:  "GET",
		uriPath: "/test.txt",
		query:   map[string]string{},
		headers: map[string]string{
			"host":  "examplebucket.s3.amazonaws.com",
			"Range": "bytes=0-9",
		},
		payload: nil,
		creds: Credentials{
			AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		region: "us-east-1",
		now:    time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC),
	}
	got := sign(in)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got != want {
		t.Errorf("簽名不符:\n got  %s\n want %s", got, want)
	}
}

// TestURIEscape 驗證 RFC3986 編碼規則。
func TestURIEscape(t *testing.T) {
	if got := uriEscape("a b/c+d~e-f_g.h", true); got != "a%20b/c%2Bd~e-f_g.h" {
		t.Errorf("keepSlash 編碼不符: %s", got)
	}
	if got := uriEscape("a/b", false); got != "a%2Fb" {
		t.Errorf("查詢編碼不符: %s", got)
	}
}

// TestRequestURL 驗證三種位址風格。
func TestRequestURL(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		wantHost string
		wantPath string
	}{
		{"AWS auto", Config{Bucket: "bkt", Region: "us-east-1"}, "bkt.s3.us-east-1.amazonaws.com", "/dir/key"},
		{"AWS path", Config{Bucket: "bkt", Region: "us-east-1", AddressingStyle: "path"}, "s3.us-east-1.amazonaws.com", "/bkt/dir/key"},
		{"custom endpoint", Config{Bucket: "bkt", Region: "us-east-1", Endpoint: "http://127.0.0.1:9000"}, "127.0.0.1:9000", "/bkt/dir/key"},
		{"custom virtual", Config{Bucket: "bkt", Region: "us-east-1", Endpoint: "http://s3.local:9000", AddressingStyle: "virtual"}, "bkt.s3.local:9000", "/dir/key"},
	}
	for _, c := range cases {
		client, err := New(c.cfg)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		host, path := client.requestURL("dir/key")
		if host != c.wantHost || path != c.wantPath {
			t.Errorf("%s: got %s%s, want %s%s", c.name, host, path, c.wantHost, c.wantPath)
		}
	}
}

// TestClientOperations 以 httptest 假 S3 驗證五個操作的請求形狀與解析。
func TestClientOperations(t *testing.T) {
	var seen struct {
		method, path, rawQuery, meta, sse string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method = r.Method
		seen.path = r.URL.Path
		seen.rawQuery = r.URL.RawQuery
		seen.meta = r.Header.Get("X-Amz-Meta-Format")
		seen.sse = r.Header.Get("X-Amz-Server-Side-Encryption")
		if r.Header.Get("Authorization") == "" || r.Header.Get("X-Amz-Date") == "" {
			t.Errorf("缺少簽名標頭")
		}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/") && r.URL.Query().Get("list-type") == "2":
			fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult>`+
				`<IsTruncated>true</IsTruncated><NextContinuationToken>tok2</NextContinuationToken>`+
				`<Contents><Key>aliyun-guard/a.agbackup</Key><Size>10</Size><LastModified>2026-09-10T00:00:00.000Z</LastModified></Contents>`+
				`<Contents><Key>aliyun-guard/b.agbackup</Key><Size>20</Size></Contents>`+
				`</ListBucketResult>`)
		case r.Method == "PUT":
			w.WriteHeader(200)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case r.Method == "HEAD":
			w.Header().Set("Content-Length", "33")
			w.WriteHeader(200)
		case r.Method == "GET" && r.URL.Path != "/":
			fmt.Fprint(w, "backup-bytes")
		}
	}))
	defer srv.Close()

	client, err := New(Config{
		Bucket: "bkt", Region: "us-east-1", Endpoint: srv.URL,
		Creds: Credentials{AccessKeyID: "ak", SecretAccessKey: "sk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// List
	page, err := client.ListObjectsV2(ctx, "aliyun-guard/", 1000, "")
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(page.Contents) != 2 || !page.Truncated || page.NextToken != "tok2" {
		t.Errorf("清單解析不符: %+v", page)
	}
	if !strings.Contains(seen.rawQuery, "list-type=2") || !strings.Contains(seen.rawQuery, "prefix=aliyun-guard%2F") {
		t.Errorf("清單查詢不符: %s", seen.rawQuery)
	}
	// Put
	if err := client.PutObject(ctx, "aliyun-guard/x.agbackup", []byte("data"),
		map[string]string{"format": "aliyun-guard-backup-v1"}, "AES256", ""); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if seen.method != "PUT" || seen.path != "/bkt/aliyun-guard/x.agbackup" || seen.meta != "aliyun-guard-backup-v1" || seen.sse != "AES256" {
		t.Errorf("PutObject 形狀不符: %+v", seen)
	}
	// Head
	size, err := client.HeadObject(ctx, "aliyun-guard/x.agbackup")
	if err != nil || size != 33 {
		t.Errorf("HeadObject: %d %v", size, err)
	}
	// Get
	data, err := client.GetObject(ctx, "aliyun-guard/x.agbackup")
	if err != nil || string(data) != "backup-bytes" {
		t.Errorf("GetObject: %s %v", data, err)
	}
	// Delete
	if err := client.DeleteObject(ctx, "aliyun-guard/x.agbackup"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if seen.method != "DELETE" {
		t.Errorf("DeleteObject 方法不符: %s", seen.method)
	}
}

// TestRetryOn500 驗證 5xx 重試後成功。
func TestRetryOn500(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	client, _ := New(Config{Bucket: "bkt", Region: "us-east-1", Endpoint: srv.URL,
		Creds: Credentials{AccessKeyID: "ak", SecretAccessKey: "sk"}})
	data, err := client.GetObject(context.Background(), "key")
	if err != nil || string(data) != "ok" {
		t.Fatalf("重試後應成功: %s %v", data, err)
	}
	if calls != 3 {
		t.Errorf("應呼叫 3 次，實得 %d", calls)
	}
}
