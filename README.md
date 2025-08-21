# tradify-cli

一个统一的 Go 命令行工具，用于将**简体中文**批量转换为**繁体中文**（缺省台湾正体 `s2twp`）。
- `mysql` 子命令：批量转换 MySQL 表指定列
- `file` 子命令：批量转换目录内文本文件

## 安装

从 Releases 页面下载对应平台的压缩包，解压后可直接运行：

```bash
./tradify-cli --help
```

或从原代码构建（需要 Go 1.25+）：

```bash
go build ./cmd/...
```

## 使用

### 根命令

```text
tradify-cli <子命令> [参数]
子命令：
  mysql   批量转换 MySQL 表指定列为繁体
  file    批量转换目录内文件内容为繁体
```

### mysql 子命令

```bash
tradify-cli mysql \
  --dsn "user:pass@tcp(127.0.0.1:3306)/mydb?charset=utf8mb4&parseTime=true" \
  --table articles --pk id --columns "title,content" \
  --to s2twp --batch-size 200 --workers 10 --dry-run
```

- `--dsn`：MySQL 连接串（必填）
- `--table`：表名（必填）
- `--pk`：主键列（可多次，支持复合主键），缺省表示**无主键表**
- `--identify-by`：无主键表用于精确定位的列
- `--columns`：要转换的列，逗号分隔（必填）
- 其它：`--to`（缺省 `s2twp`）、`--batch-size`、`--workers`、`--rps`、`--dry-run`

### file 子命令

```bash
tradify-cli file --dir . --ext ".txt,.md" --dry-run
tradify-cli file --dir /var/www --ext ".php" --backup
```

- `--dir`：根目录（缺省当前目录）
- `--ext`：过滤扩展名（逗号分隔；留空表示全部）
- `--to`：OpenCC 配置（缺省 `s2twp`）
- `--backup`：写回前保存 `.bak` 备份
- `--dry-run`：试运行，不修改任何文件
- `--workers`：并发数量（缺省 4）

## 注意事项
- 转换使用 [OpenCC](https://github.com/BYVoid/OpenCC) 的 Go 实现 `github.com/longbridgeapp/opencc`。
- 对纯 ASCII 或不含汉字的文本自动跳过，避免无谓转换。
- 数据库写入采用参数化更新，`--dry-run` 会打印计划运行的 SQL 而不落库。
- **无主键表**更新会较慢，建议提供 `--identify-by` 唯一列提升准确性与性能。

## 许可
MIT
