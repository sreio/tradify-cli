# tradify-cli

一个统一的 Go 命令行工具，用于将**简体中文**批量转换为**繁体中文**（默认台湾正体 `s2twp`）。
- `mysql` 子命令：批量转换 MySQL 表指定列（支持 **配置文件** 批量多表）
- `file` 子命令：批量转换目录内文本文件

## 安装

从 Releases 页面下载对应平台的压缩包，解压后可直接运行：

```bash
./tradify-cli --help
```

或从源码构建（需要 Go 1.25+）：

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

---

## mysql 子命令

### 方式一：配置文件模式（推荐，多表多字段）

- 生成模板：
  ```bash
  tradify-cli mysql gen-config --dir ./configs
  ```

- 编辑生成的 `tradify_config_template.json`，按注释/示例填写。

- 执行：
  ```bash
  # 目录模式：批量执行目录下所有 *.json
  tradify-cli mysql --conf ./configs

  # 单文件模式：仅执行指定 JSON
  tradify-cli mysql --conf ./configs/myjob.json
  ```

> 注意：配置文件方式与命令行单表参数互斥，使用 `--conf` 时将**忽略** `--table/--columns/...`。  
> 配置文件不支持被命令行覆盖，请直接在 JSON 中写好所有参数。

### 方式二：单表快速模式

```bash
tradify-cli mysql \
  --dsn "user:pass@tcp(127.0.0.1:3306)/mydb?charset=utf8mb4&parseTime=true" \
  --table articles --pk id --columns "title,content" \
  --to s2twp --batch-size 200 --workers 10 --dry-run true
```

- `--dsn`：MySQL 连接串（必填）
- `--table`：表名（必填）
- `--pk`：主键列（可多次，支持复合主键）
- `--identify-by`：无主键表用于精确定位的列
- `--columns`：要转换的列，逗号分隔（必填）
- 其它：`--to`（默认 `s2twp`）、`--batch-size`、`--workers`、`--rps`、`--dry-run`、`--max-open`、`--max-idle`、`--conn-max-lifetime`

---

## 配置文件格式（JSON，snake_case）

顶层全局字段：

- `dsn` (必填)
- `to`（默认 `s2twp`）
- `batch_size`（默认 500）
- `workers`（默认 8）
- `rps`（默认 0 不限速）
- `dry_run`（默认 `true`）
- `max_open`（默认 200）
- `max_idle`（默认 20）
- `conn_max_lifetime`（默认 `"30m"`）
- `tables_parallel` 同时并发处理的表数量（默认1）
- `tables`：数组，每个元素是一个表配置对象：
    - `table` (必填) 表名
    - `pk`（可选）主键列数组（支持复合主键）
    - `identify_by`（可选）无主键表的定位列
    - `columns` (必填) 需要转换的列名数组
    - `workers`/`batch_size`/`rps`（可选）表级覆盖

示例（节选）：
```json
{
  "dsn": "root:123456@tcp(127.0.0.1:3306)/yourdb?charset=utf8mb4&parseTime=true",
  "to": "s2twp",
  "batch_size": 500,
  "workers": 8,
  "rps": 0,
  "dry_run": true,
  "max_open": 200,
  "max_idle": 20,
  "conn_max_lifetime": "30m",
  "tables_parallel": 1,
  "tables": [
    { "table": "posts", "pk": ["id"], "columns": ["title", "content"], "workers": 12, "batch_size": 800 },
    { "table": "orders", "pk": ["order_id","item_id"], "columns": ["remark"] },
    { "table": "comments", "identify_by": ["uuid"], "columns": ["body"] },
    { "table": "legacy_table", "columns": ["desc"] }
  ]
}
```

---

## file 子命令

```bash
tradify-cli file --dir . --ext ".txt,.md" --dry-run true
tradify-cli file --dir /var/www --ext ".php" --backup --dry-run false
```

- `--dir`：根目录（默认当前目录）
- `--ext`：过滤扩展名（逗号分隔；留空表示全部）
- `--to`：OpenCC 配置（默认 `s2twp`）
- `--backup`：写回前保存 `.bak` 备份
- `--dry-run`：试运行，不修改任何文件
- `--workers`：并发数量（默认 4）

## 许可
MIT
