// 遠端清單/清理/下載/連線測試，對應 test_connection / list_backups /
// prune_backups / download_backup / _validate_remote_key。
package s3backup

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"aliyun-guard-go/internal/backup"
	"aliyun-guard-go/internal/s3"
)

// TestConnection 連線測試（ListObjectsV2 MaxKeys=1），回傳結果 dict。
func TestConnection(value map[string]any) (map[string]any, error) {
	ready := true
	cfg, err := ValidateConfig(value, &ready)
	if err != nil {
		return nil, err
	}
	client, err := NewS3Client(clientConfig(cfg))
	if err != nil {
		return nil, err
	}
	started := time.Now()
	if _, err := client.ListObjectsV2(context.Background(), cfg.ObjectPrefix(), 1, ""); err != nil {
		return nil, errf("S3 连接测试失败: %s", CompactError(err, cfg.Secrets()))
	}
	endpoint := cfg.EndpointURL
	if endpoint == "" {
		endpoint = "AWS S3"
	}
	return map[string]any{
		"ok":         true,
		"bucket":     cfg.Bucket,
		"region":     cfg.Region,
		"endpoint":   endpoint,
		"latency_ms": int(time.Since(started).Milliseconds()),
	}, nil
}

// BackupItem 為清單項目（key/name/size/modified_at/etag）。
type BackupItem struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
	ETag       string `json:"etag"`
}

// ListBackups 分頁列出前綴下的備份物件，過濾含 / 與非 .agbackup，
// 依（modified_at, key）倒序，上限 limit。對應 list_backups。
// client 為 nil 時自動建立。
func ListBackups(cfg *Config, client *s3.Client, limit int) ([]BackupItem, error) {
	return listBackupsImpl(cfg, client, limit)
}

func listBackupsImpl(cfg *Config, client *s3.Client, limit int) ([]BackupItem, error) {
	if client == nil {
		var err error
		client, err = NewS3Client(clientConfig(cfg))
		if err != nil {
			return nil, err
		}
	}
	prefix := cfg.ObjectPrefix()
	var items []BackupItem
	token := ""
	for len(items) < MaxRemoteItems {
		page, err := client.ListObjectsV2(context.Background(), prefix, 1000, token)
		if err != nil {
			return nil, errf("读取 S3 备份列表失败: %s", CompactError(err, cfg.Secrets()))
		}
		for _, obj := range page.Contents {
			name := ""
			if strings.HasPrefix(obj.Key, prefix) {
				name = obj.Key[len(prefix):]
			}
			if name == "" || strings.Contains(name, "/") || !strings.HasSuffix(obj.Key, ".agbackup") {
				continue
			}
			etag := strings.Trim(obj.ETag, "\"")
			items = append(items, BackupItem{
				Key:        obj.Key,
				Name:       name,
				Size:       obj.Size,
				ModifiedAt: obj.LastModified.Format("2006-01-02T15:04:05Z07:00"),
				ETag:       etag,
			})
		}
		if !page.Truncated || page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ModifiedAt != items[j].ModifiedAt {
			return items[i].ModifiedAt > items[j].ModifiedAt
		}
		return items[i].Key > items[j].Key
	})
	if limit < 1 {
		limit = 1
	}
	if limit > MaxRemoteItems {
		limit = MaxRemoteItems
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// PruneBackups 刪除超過 retention 的遠端備份，回傳被刪 key 清單。
func PruneBackups(cfg *Config, client *s3.Client) ([]string, error) {
	items, err := listBackupsImpl(cfg, client, MaxRemoteItems)
	if err != nil {
		return nil, err
	}
	deleted := []string{}
	if cfg.Retention < len(items) {
		for _, item := range items[cfg.Retention:] {
			if err := client.DeleteObject(context.Background(), item.Key); err != nil {
				return nil, errf("清理旧 S3 备份失败: %s", CompactError(err, cfg.Secrets()))
			}
			deleted = append(deleted, item.Key)
		}
	}
	return deleted, nil
}

// ValidateRemoteKey 驗證備份物件位於當前前綴下，對應 _validate_remote_key。
func ValidateRemoteKey(cfg *Config, key string) (string, string, error) {
	prefix := cfg.ObjectPrefix()
	key = strings.TrimSpace(key)
	name := ""
	if strings.HasPrefix(key, prefix) {
		name = key[len(prefix):]
	}
	if name == "" || len(key) > 1024 || len(name) > 255 ||
		strings.Contains(name, "/") || strings.Contains(name, "\\") ||
		!strings.HasSuffix(name, ".agbackup") {
		return "", "", errf("S3 备份对象不在当前配置前缀下")
	}
	return key, name, nil
}

// DownloadBackup 下載遠端備份到本機 backups/ 目錄（原子替換、0600），
// 回傳本機路徑。對應 download_backup。
func DownloadBackup(value map[string]any, key, appDir string) (string, error) {
	ready := true
	cfg, err := ValidateConfig(value, &ready)
	if err != nil {
		return "", err
	}
	key, name, err := ValidateRemoteKey(cfg, key)
	if err != nil {
		return "", err
	}
	_ = name
	directory := filepath.Join(appDir, "backups")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	destination := filepath.Join(directory, ".s3-download-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".agbackup")
	tmp := destination + ".tmp"
	cleanup := func() {
		_ = os.Remove(destination)
		_ = os.Remove(tmp)
	}
	client, err := NewS3Client(clientConfig(cfg))
	if err != nil {
		cleanup()
		return "", err
	}
	size, err := client.HeadObject(context.Background(), key)
	if err != nil {
		cleanup()
		return "", errf("读取 S3 备份信息失败: %s", CompactError(err, cfg.Secrets()))
	}
	if size > backup.MaxBackupFileBytes {
		cleanup()
		return "", errf("S3 备份文件超过大小限制")
	}
	data, err := client.GetObject(context.Background(), key)
	if err != nil {
		cleanup()
		return "", errf("下载 S3 加密备份失败: %s", CompactError(err, cfg.Secrets()))
	}
	if len(data) > backup.MaxBackupFileBytes {
		cleanup()
		return "", errf("S3 备份文件超过大小限制")
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		cleanup()
		return "", err
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, destination); err != nil {
		cleanup()
		return "", err
	}
	return destination, nil
}
