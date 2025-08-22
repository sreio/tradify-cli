package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vbauerster/mpb/v8"
)

// 配置文件结构（JSON，使用 snake_case 字段名）
type MySQLFileConfig struct {
	DSN             string          `json:"dsn"`
	To              string          `json:"to"`
	BatchSize       int             `json:"batch_size"`
	Workers         int             `json:"workers"`
	RPS             int             `json:"rps"`
	DryRun          bool            `json:"dry_run"`
	MaxOpenConns    int             `json:"max_open"`
	MaxIdleConns    int             `json:"max_idle"`
	ConnMaxLifetime string          `json:"conn_max_lifetime"` // e.g. "30m"
	TablesParallel  int             `json:"tables_parallel"`   // 同时并发处理的表数量（默认1）
	Tables          []MySQLTblEntry `json:"tables"`
}

// 单表条目（支持主键 pk、无主键 identify_by、及表级覆盖 batch_size/workers/rps）
type MySQLTblEntry struct {
	Table      string   `json:"table"`
	PK         []string `json:"pk,omitempty"`
	IdentifyBy []string `json:"identify_by,omitempty"`
	Columns    []string `json:"columns"`

	BatchSize int `json:"batch_size,omitempty"`
	Workers   int `json:"workers,omitempty"`
	RPS       int `json:"rps,omitempty"`
}

// 解析单个 JSON 配置文件
func LoadMySQLFileConfig(path string) (*MySQLFileConfig, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg MySQLFileConfig
	if err := json.Unmarshal(bs, &cfg); err != nil {
		return nil, fmt.Errorf("json parse %s: %w", path, err)
	}
	// 基本校验 & 默认值
	if cfg.DSN == "" {
		return nil, errors.New("配置缺少 dsn")
	}
	if cfg.To == "" {
		cfg.To = "s2twp"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 8
	}
	if strings.TrimSpace(cfg.ConnMaxLifetime) == "" {
		cfg.ConnMaxLifetime = "30m"
	}
	if cfg.TablesParallel <= 0 {
		cfg.TablesParallel = 1
	}
	if len(cfg.Tables) == 0 {
		return nil, errors.New("配置缺少 tables")
	}
	for i := range cfg.Tables {
		if cfg.Tables[i].Table == "" {
			return nil, fmt.Errorf("tables[%d] 缺少 table", i)
		}
		if len(cfg.Tables[i].Columns) == 0 {
			return nil, fmt.Errorf("tables[%s] 缺少 columns", cfg.Tables[i].Table)
		}
	}
	return &cfg, nil
}

// 根据文件配置执行所有表（支持并发 & 多进度条）
func RunMySQLFromFileConfig(fileCfg *MySQLFileConfig, baseDir string) error {
	// 解析连接生命周期
	dur, err := time.ParseDuration(fileCfg.ConnMaxLifetime)
	if err != nil {
		return fmt.Errorf("解析 conn_max_lifetime 失败：%w", err)
	}

	// 多表并发控制
	sem := make(chan struct{}, fileCfg.TablesParallel)
	var wg sync.WaitGroup

	// 多进度条容器
	p := mpb.New(mpb.WithWidth(60), mpb.WithWaitGroup(&wg))

	// 错误收集
	errCh := make(chan error, len(fileCfg.Tables))

	for _, t := range fileCfg.Tables {
		// 表级覆盖
		batch := fileCfg.BatchSize
		if t.BatchSize > 0 {
			batch = t.BatchSize
		}
		workers := fileCfg.Workers
		if t.Workers > 0 {
			workers = t.Workers
		}
		rps := fileCfg.RPS
		if t.RPS > 0 {
			rps = t.RPS
		}
		cfg := MySQLConfig{
			DSN:             fileCfg.DSN,
			Table:           t.Table,
			PK:              t.PK,
			IdentifyBy:      t.IdentifyBy,
			Columns:         t.Columns,
			To:              fileCfg.To,
			BatchSize:       batch,
			Workers:         workers,
			RPS:             rps,
			DryRun:          fileCfg.DryRun,
			MaxOpenConns:    fileCfg.MaxOpenConns,
			MaxIdleConns:    fileCfg.MaxIdleConns,
			ConnMaxLifetime: dur,
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(cfg MySQLConfig) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := RunMySQLWithProgress(cfg, p); err != nil {
				errCh <- fmt.Errorf("table %s: %w", cfg.Table, err)
			}
		}(cfg)
	}

	// 等待所有任务 & 进度条结束
	wg.Wait()
	p.Wait()
	close(errCh)

	// 返回第一个错误（若有）
	for e := range errCh {
		return e
	}
	return nil
}

// 解析 --conf 目标（保持不变）
func ResolveConfigTargets(conf string) ([]string, error) {
	target := conf
	if strings.TrimSpace(target) == "" {
		target = "."
	}
	stat, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", target, err)
	}
	if stat.IsDir() {
		var list []string
		err := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
				list = append(list, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return list, nil
	}
	if strings.HasSuffix(strings.ToLower(target), ".json") {
		return []string{target}, nil
	}
	return nil, fmt.Errorf("不支持的 --conf 目标（需为 .json 文件或目录）：%s", target)
}

// 生成配置模板 JSON（含字段解释与示例）
func GenerateConfigTemplate(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "tradify_config_template.json")
	template := map[string]interface{}{
		"_说明": map[string]interface{}{
			"dsn":                  `MySQL 连接串 (必填)，示例：user:pass@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=true`,
			"to":                   `OpenCC 转换配置，默认 s2twp（简体->繁体（台湾））`,
			"batch_size":           "每批处理行数，默认 500",
			"workers":              "全局并发 worker 数，默认 8；若表条目提供同名字段则优先生效",
			"rps":                  "全局限速（每秒最大处理行数），默认 0 不限速",
			"dry_run":              "试运行，true=只打印更新不落库；false=真实写入",
			"max_open":             "数据库最大打开连接数，默认 200",
			"max_idle":             "数据库最大空闲连接数，默认 20",
			"conn_max_lifetime":    "连接最大生命周期（Go duration），默认 30m",
			"tables_parallel":      "同时并发处理的表数量（默认1）",
			"tables[].table":       "表名（必填）",
			"tables[].pk":          "主键列数组，可单列或复合主键（可选）",
			"tables[].identify_by": "无主键时用于定位行的列（可选）。若均未提供，将退化为整行匹配（最慢，不推荐）",
			"tables[].columns":     "需要转换的列名数组（必填）",
			"tables[].workers":     "表级并发覆盖（可选）",
			"tables[].batch_size":  "表级批大小覆盖（可选）",
			"tables[].rps":         "表级限速覆盖（可选）",
		},
		"dsn":               `root:123456@tcp(127.0.0.1:3306)/yourdb?charset=utf8mb4&parseTime=true`,
		"to":                "s2twp",
		"batch_size":        500,
		"workers":           8,
		"rps":               0,
		"dry_run":           true,
		"max_open":          200,
		"max_idle":          20,
		"conn_max_lifetime": "30m",
		"tables_parallel":   1,
		"tables": []map[string]interface{}{
			{
				"table":      "posts",
				"pk":         []string{"id"},
				"columns":    []string{"title", "content"},
				"workers":    12,
				"rps":        0,
				"batch_size": 800,
			},
			{
				"table":      "orders",
				"pk":         []string{"order_id", "item_id"},
				"columns":    []string{"remark"},
				"batch_size": 500,
			},
			{
				"table":       "comments",
				"identify_by": []string{"uuid"},
				"columns":     []string{"body"},
			},
			{
				"table":   "legacy_table",
				"columns": []string{"desc"},
			},
		},
	}

	// 用 Encoder 并关闭 HTML 转义
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // 关键：避免 & < > 转义
	if err := enc.Encode(template); err != nil {
		return "", err
	}

	return out, nil
}
