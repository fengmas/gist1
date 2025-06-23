package save

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"testing"

	"github.com/beck-8/subs-check/check"
)

func TestGenUrls(t *testing.T) {
	tests := []struct {
		name    string
		input   []map[string]any
		want    string
		wantErr bool
	}{
		{
			name: "valid hysteria2 config",
			input: []map[string]any{
				{
					"type":     "hysteria2",
					"uuid":     "b82f14be-9225-48cb-963e-0350c86c31d3",
					"server":   "us2.interld123456789.com",
					"port":     32000,
					"name":     "美国hy2-2-联通电信",
					"insecure": 1,
					"sni":      "234224.1234567890spcloud.com",
					"mport":    "32000-33000",
				},
			},
			want:    "hysteria2://b82f14be-9225-48cb-963e-0350c86c31d3@us2.interld123456789.com:32000?insecure=1&mport=32000-33000&sni=234224.1234567890spcloud.com#美国hy2-2-联通电信\n",
			wantErr: false,
		},
		{
			name: "multiple configs",
			input: []map[string]any{
				{
					"type":     "hysteria2",
					"uuid":     "b82f14be-9225-48cb-963e-0350c86c31d3",
					"server":   "us2.interld123456789.com",
					"port":     32000,
					"name":     "美国hy2-2-联通电信",
					"insecure": 1,
					"sni":      "234224.1234567890spcloud.com",
				},
			},
			want:    "hysteria2://b82f14be-9225-48cb-963e-0350c86c31d3@us2.interld123456789.com:32000?insecure=1&sni=234224.1234567890spcloud.com#美国hy2-2-联通电信\n",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("Failed to marshal input: %v", err)
			}

			got, err := genUrls(data)

			if (err != nil) != tt.wantErr {
				t.Errorf("genUrls() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			parsedGot, _ := url.Parse(got.String())

			// 重建 URL 进行比较
			gotDecoded := fmt.Sprintf("%s://%s@%s%s?%s#%s",
				parsedGot.Scheme,
				parsedGot.User.String(),
				parsedGot.Host,
				parsedGot.Path,
				parsedGot.RawQuery,
				parsedGot.Fragment)
			if gotDecoded != tt.want {
				t.Errorf("genUrls() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGenerateTimestampFilename 测试时间戳文件名生成功能
func TestGenerateTimestampFilename(t *testing.T) {
	// 测试生成的文件名格式
	filename := generateTimestampFilename()

	// 验证文件名格式：all_20230615120000.yaml
	matched, err := regexp.MatchString(`^all_\d{14}\.yaml$`, filename)
	if err != nil {
		t.Fatalf("正则表达式匹配失败: %v", err)
	}

	if !matched {
		t.Errorf("生成的文件名格式不正确: %s, 期望格式: all_YYYYMMDDHHMMSS.yaml", filename)
	}

	t.Logf("生成的文件名: %s", filename)
}

// TestConfigSaverCategories 测试不同配置保存器的文件类别
func TestConfigSaverCategories(t *testing.T) {
	// 创建测试数据
	results := []check.Result{
		{
			Proxy: map[string]any{
				"name":   "test-proxy",
				"type":   "ss",
				"server": "example.com",
				"port":   8080,
			},
		},
	}

	// 测试普通配置保存器（不包含时间戳文件）
	normalSaver := NewConfigSaver(results)
	normalCategories := normalSaver.categories

	// 测试本地配置保存器（包含时间戳文件）
	localSaver := NewLocalConfigSaver(results)
	localCategories := localSaver.categories

	// 验证两个保存器的基本文件数量差异
	expectedDiff := 1 // 本地保存器应该比普通保存器多一个时间戳文件
	actualDiff := len(localCategories) - len(normalCategories)

	if actualDiff != expectedDiff {
		t.Errorf("本地保存器应该比普通保存器多%d个文件，实际差异: %d", expectedDiff, actualDiff)
	}

	t.Logf("普通保存器文件数量: %d", len(normalCategories))
	t.Logf("本地保存器文件数量: %d", len(localCategories))
}
