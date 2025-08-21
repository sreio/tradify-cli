package internal

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type FileConfig struct {
	RootDir string
	Exts    []string // 过滤扩展名（含点），为空表示全部
	To      string
	Backup  bool
	DryRun  bool
	Workers int
}

func RunFile(cfg FileConfig) error {
	if cfg.RootDir == "" {
		cfg.RootDir = "."
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}

	// 规范化扩展名到小写
	extSet := map[string]struct{}{}
	for _, e := range cfg.Exts {
		e = strings.TrimSpace(strings.ToLower(e))
		if e != "" && !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		if e != "" {
			extSet[e] = struct{}{}
		}
	}

	type task struct{ path string }
	ch := make(chan task, 128)

	var wg sync.WaitGroup
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range ch {
				if err := processFile(t.path, cfg, extSet); err != nil {
					log.Printf("[file] %v", err)
				}
			}
		}()
	}

	// walk 目录
	err := filepath.WalkDir(cfg.RootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("[file] walk error: %v", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if len(extSet) > 0 {
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if _, ok := extSet[ext]; !ok {
				return nil
			}
		}
		ch <- task{path: path}
		return nil
	})
	close(ch)
	wg.Wait()

	return err
}

func processFile(path string, cfg FileConfig, extSet map[string]struct{}) error {
	bs, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取失败 %s: %w", path, err)
	}
	orig := string(bs)

	out, need, err := ConvertIfNeeded(cfg.To, orig)
	if err != nil {
		return fmt.Errorf("转换失败 %s: %w", path, err)
	}
	if !need {
		return nil
	}

	if cfg.DryRun {
		log.Printf("[DRYRUN] 将修改文件：%s", path)
		return nil
	}

	// 备份
	if cfg.Backup {
		if err := os.WriteFile(path+".bak", bs, 0644); err != nil {
			return fmt.Errorf("写备份失败 %s.bak: %w", path, err)
		}
	}

	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return fmt.Errorf("写回失败 %s: %w", path, err)
	}
	log.Printf("[OK] 转换完成：%s", path)
	return nil
}
