package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sreio/tradify-cli/internal"
)

// 统一入口 CLI：根据第一个参数分发到子命令
func main() {
	if len(os.Args) < 2 {
		printRootHelp()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "mysql":
		runMySQL(os.Args[2:])
	case "file":
		runFile(os.Args[2:])
	case "-h", "--help", "help":
		printRootHelp()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %q\n\n", os.Args[1])
		printRootHelp()
		os.Exit(2)
	}
}

func printRootHelp() {
	fmt.Println(`tradify-cli - 简繁体批量转换工具

[github]: https://github.com/sreio/tradify-cli

用法：
  tradify-cli <子命令> [参数]

子命令：
  mysql   批量转换 MySQL 表指定列为繁体（支持配置文件 & 模板生成）
  file    批量转换目录内文档内容为繁体

查看子命令帮助：
  tradify-cli mysql --help
  tradify-cli file  --help
`)
}

// -------------- mysql 子命令 --------------

func runMySQL(args []string) {
	// 子子命令：mysql gen-config
	if len(args) > 0 && args[0] == "gen-config" {
		runGenConfig(args[1:])
		return
	}

	fs := flag.NewFlagSet("mysql", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		// 配置文件模式
		confPath = fs.String("conf", "", "【可选】配置文件或目录路径：指定文件(如 a.json)或目录(批量执行目录下 *.json)")
		// 单表直接参数模式（与 --conf 互斥）
		dsn        = fs.String("dsn", "", "【必填】MySQL 连接串，例如：user:pass@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=true")
		table      = fs.String("table", "", "【必填】表名")
		columnsStr = fs.String("columns", "", "【必填】要转换的列名，逗号分隔，如：name,content")
		to         = fs.String("to", "s2twp", "OpenCC 转换配置（默认 s2twp），可选如：s2t、t2s 等")
		batchSize  = fs.Int("batch-size", 500, "每批处理行数（默认 500）")
		workers    = fs.Int("workers", 8, "并发 worker 数（默认 8）")
		rps        = fs.Int("rps", 0, "每秒最大处理行数（默认 0 不限速）")
		dryRun     = fs.Bool("dry-run", true, "试运行：不落库，仅打印将运行的更新")
		maxOpen    = fs.Int("max-open", 200, "数据库最大打开连接数（默认200）")
		maxIdle    = fs.Int("max-idle", 20, "数据库最大空闲连接数（默认20）")
		connLife   = fs.Duration("conn-max-lifetime", 30*time.Minute, "单连接最大生命周期（默认30m）")
	)

	var pks multiCSV
	var idBy multiCSV
	fs.Var(&pks, "pk", "主键列名（可多次指定或逗号分隔，支持复合主键）")
	fs.Var(&idBy, "identify-by", "无主键时用于定位的列（可多次指定或逗号分隔）")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `用法：
  1) 配置文件模式（推荐，多表多字段）：
     tradify-cli mysql --conf ./configs           # 目录下所有 *.json
     tradify-cli mysql --conf ./tradify-config.json

  2) 单表模式（快速执行一个表）：
     tradify-cli mysql --dsn "user:pass@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=true" \
       --table articles --pk id --columns "title,content" --to s2twp --batch-size 200 --workers 10 --dry-run=true

  3) 生成配置模板：
     tradify-cli mysql gen-config --dir ./configs

说明：
  - 配置文件模式与单表模式**互斥**。若提供 --conf，将忽略 --table/--columns 等单表参数。
  - 配置文件使用 JSON，支持全局参数与表级覆盖；配置方式不支持被命令行覆盖。

参数（单表模式）：
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
示例（复合主键 & 真实写入）：
  tradify-cli mysql --dsn "user:pass@tcp(127.0.0.1:3306)/mydb" \
    --table your_table --pk pk1 --pk pk2 --columns "colA,colB" --rps 50 --dry-run=false
`)
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	// 如果使用 --conf，则走配置文件模式
	if *confPath != "" {
		paths, err := internal.ResolveConfigTargets(*confPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取配置失败：%v\n", err)
			os.Exit(1)
		}
		if len(paths) == 0 {
			fmt.Fprintln(os.Stderr, "未在目标找到任何 .json 配置文件")
			os.Exit(2)
		}
		for _, p := range paths {
			cfg, err := internal.LoadMySQLFileConfig(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "解析配置失败 %s：%v\n", p, err)
				os.Exit(1)
			}
			if err := internal.RunMySQLFromFileConfig(cfg, filepath.Dir(p)); err != nil {
				fmt.Fprintf(os.Stderr, "执行失败（配置 %s）：%v\n", p, err)
				os.Exit(1)
			}
		}
		return
	}

	// 单表模式校验
	if *dsn == "" || *table == "" || *columnsStr == "" {
		fs.Usage()
		os.Exit(2)
	}

	cfg := internal.MySQLConfig{
		DSN:             *dsn,
		Table:           *table,
		PK:              pks.Values(),
		IdentifyBy:      idBy.Values(),
		Columns:         internal.SplitCSV(*columnsStr),
		To:              *to,
		BatchSize:       *batchSize,
		Workers:         *workers,
		RPS:             *rps,
		DryRun:          *dryRun,
		MaxOpenConns:    *maxOpen,
		MaxIdleConns:    *maxIdle,
		ConnMaxLifetime: *connLife,
	}

	if err := internal.RunMySQL(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "运行失败：%v\n", err)
		os.Exit(1)
	}
}

// -------------- mysql gen-config --------------

func runGenConfig(args []string) {
	fs := flag.NewFlagSet("mysql gen-config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", ".", "模板生成目录（默认当前目录）")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `用法：tradify-cli mysql gen-config [--dir 目录]

说明：
  在指定目录生成 JSON 配置模板（含字段解释与示例）。

示例：
  tradify-cli mysql gen-config --dir ./configs
`)
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	path, err := internal.GenerateConfigTemplate(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成模板失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("模板已生成：%s\n", path)
}

// -------------- file 子命令 --------------

func runFile(args []string) {
	fs := flag.NewFlagSet("file", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		dir     = fs.String("dir", ".", "【必填】要处理的根目录路径（默认当前目录）")
		extsCSV = fs.String("ext", "", "过滤的文档扩展名（可逗号分隔，如：.txt,.md；留空表示处理所有文档）")
		to      = fs.String("to", "s2twp", "OpenCC 转换配置（默认 s2twp）")
		backup  = fs.Bool("backup", false, "是否对每个被修改的文档生成 .bak 备份（默认 false）")
		dryRun  = fs.Bool("dry-run", true, "试运行：不写回，仅列出将被修改的文档")
		workers = fs.Int("workers", 4, "并发 worker 数（缺省 4）")
	)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `用法：tradify-cli file [参数...]

说明：
  递归遍历目录，将匹配扩展名的文档内容从简体转换为繁体（默认 s2twp）。
  支持 dry-run 试运行与备份。

参数：
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
示例：
  1) 处理当前目录所有 .txt 与 .md 文档，先试运行：
     tradify-cli file --dir . --ext ".txt,.md" --dry-run=true

  2) 实际写回并按需备份：
     tradify-cli file --dir /var/www --ext ".php" --backup --dry-run=false
`)
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	exts := internal.SplitCSV(*extsCSV)
	cfg := internal.FileConfig{
		RootDir: *dir,
		Exts:    exts,
		To:      *to,
		Backup:  *backup,
		DryRun:  *dryRun,
		Workers: *workers,
	}

	if err := internal.RunFile(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "运行失败：%v\n", err)
		os.Exit(1)
	}
}

// --------- 工具：支持 --pk/--identify-by 多次/逗号混用 ---------

type multiCSV struct{ items []string }

func (m *multiCSV) String() string { return fmt.Sprint(m.items) }
func (m *multiCSV) Set(v string) error {
	for _, s := range internal.SplitCSV(v) {
		m.items = append(m.items, s)
	}
	return nil
}
func (m *multiCSV) Values() []string { return append([]string(nil), m.items...) }
