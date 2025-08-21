package main

import (
	"flag"
	"fmt"
	"os"

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
	fmt.Println(`tradify-cli - 简繁体批量转换工具（统一入口）

[github]: https://github.com/sreio/tradify-cli

用法：
  tradify-cli <子命令> [参数]

子命令：
  mysql   批量转换 MySQL 表指定列为繁体
  file    批量转换目录内文档内容为繁体

查看子命令帮助：
  tradify-cli mysql --help
  tradify-cli file  --help
`)
}

// -------------- mysql 子命令 --------------

func runMySQL(args []string) {
	fs := flag.NewFlagSet("mysql", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		dsn        = fs.String("dsn", "", "【必填】MySQL 连接串，例如：user:pass@tcp(127.0.0.1:3306)/db?charset=utf8mb4&parseTime=true")
		table      = fs.String("table", "", "【必填】表名")
		columnsStr = fs.String("columns", "", "【必填】要转换的列名，逗号分隔，如：name,content")
		to         = fs.String("to", "s2twp", "OpenCC 转换配置（缺省 s2twp），可选如：s2t、t2s 等")
		batchSize  = fs.Int("batch-size", 500, "每批处理行数（缺省 500）")
		workers    = fs.Int("workers", 8, "并发 worker 数（缺省 8）")
		rps        = fs.Int("rps", 0, "每秒最大处理行数（缺省 0 不限速）")
		dryRun     = fs.Bool("dry-run", true, "试运行：不落库，仅打印将运行的更新")
	)

	var pks multiCSV
	var idBy multiCSV
	fs.Var(&pks, "pk", "主键列名（可多次指定或逗号分隔，支持复合主键）")
	fs.Var(&idBy, "identify-by", "无主键时用于定位的列（可多次指定或逗号分隔）")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `用法：tradify-cli mysql [参数...]

说明：
  批量将 MySQL 表中指定列的文本从简体转换为繁体（缺省 s2twp）。
  支持 dry-run 试运行、并发处理、RPS 限速、复合主键增量遍历。

参数：
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
示例：
  1) 以主键 id 增量处理，试运行：
     tradify-cli mysql --dsn "user:pass@tcp(127.0.0.1:3306)/mydb?charset=utf8mb4&parseTime=true" \
       --table articles --pk id --columns "title,content" --to s2twp --batch-size 200 --workers 10 --dry-run true

  2) 复合主键（pk1,pk2），真实写入：
     tradify-cli mysql --dsn "user:pass@tcp(127.0.0.1:3306)/mydb" \
       --table your_table --pk pk1 --pk pk2 --columns "colA,colB" --rps 50 --dry-run false

  3) 无主键表，使用 identify-by 列定位并更新（每次 WHERE 精确定位单行）：
     tradify-cli mysql --dsn "user:pass@tcp(127.0.0.1:3306)/mydb" \
       --table no_pk_table --identify-by uniq_col --columns "name,desc" --dry-run false
`)
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *dsn == "" || *table == "" || *columnsStr == "" {
		fs.Usage()
		os.Exit(2)
	}

	cfg := internal.MySQLConfig{
		DSN:        *dsn,
		Table:      *table,
		PK:         pks.Values(),
		IdentifyBy: idBy.Values(),
		Columns:    internal.SplitCSV(*columnsStr),
		To:         *to,
		BatchSize:  *batchSize,
		Workers:    *workers,
		RPS:        *rps,
		DryRun:     *dryRun,
	}

	if err := internal.RunMySQL(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "运行失败：%v\n", err)
		os.Exit(1)
	}
}

// -------------- file 子命令 --------------

func runFile(args []string) {
	fs := flag.NewFlagSet("file", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		dir     = fs.String("dir", ".", "【必填】要处理的根目录路径（缺省当前目录）")
		extsCSV = fs.String("ext", "", "过滤的文档扩展名（可逗号分隔，如：.txt,.md；留空表示处理所有文档）")
		to      = fs.String("to", "s2twp", "OpenCC 转换配置（缺省 s2twp）")
		backup  = fs.Bool("backup", false, "是否对每个被修改的文档生成 .bak 备份（缺省 false）")
		dryRun  = fs.Bool("dry-run", true, "试运行：不写回，仅列出将被修改的文档")
		workers = fs.Int("workers", 4, "并发 worker 数（缺省 4）")
	)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `用法：tradify-cli file [参数...]

说明：
  递归遍历目录，将匹配扩展名的文档内容从简体转换为繁体（缺省 s2twp）。
  支持 dry-run 试运行与备份。

参数：
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
示例：
  1) 处理当前目录所有 .txt 与 .md 文档，先试运行：
     tradify-cli file --dir . --ext ".txt,.md" --dry-run true

  2) 实际写回并按需备份：
     tradify-cli file --dir /var/www --ext ".php" --backup --dry-run false
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

// False() 为 flag.Bool 默认值书写小助手
func False() *bool { b := false; return &b }
