package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

type MySQLConfig struct {
	DSN             string
	Table           string
	PK              []string // 支持复合主键；为空表示无主键
	IdentifyBy      []string // 无主键时用于 WHERE 定位的列
	Columns         []string
	To              string
	BatchSize       int
	Workers         int // 预留：后续可做每表内部并发
	RPS             int
	DryRun          bool
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// 单表模式：内部创建一个进度容器
func RunMySQL(cfg MySQLConfig) error {
	p := mpb.New(
		mpb.WithWidth(60),
		mpb.WithOutput(os.Stdout), // 进度条只往 STDOUT
		mpb.WithRefreshRate(120*time.Millisecond),
	)
	defer p.Wait()
	return RunMySQLWithProgress(cfg, p)
}

// 多表模式：外部传入进度容器（便于多条进度条并发显示）
func RunMySQLWithProgress(cfg MySQLConfig, p *mpb.Progress) error {
	if len(cfg.Columns) == 0 {
		return errors.New("必须提供 --columns")
	}

	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	if err := db.Ping(); err != nil {
		return fmt.Errorf("db ping: %w", err)
	}

	// 统计总行数（用于进度条总量）
	total, err := countTotalRows(db, cfg.Table)
	if err != nil {
		// 统计失败则使用“动态总量”模式
		total = -1
	}

	// 进度条（每表一条）
	var bar *mpb.Bar
	if p != nil {
		if total > 0 {
			// 总量已知
			bar = p.AddBar(
				total,
				mpb.PrependDecorators(
					decor.Name("["+cfg.Table+"] "),
					decor.CountersNoUnit("%d/%d"),
					decor.Percentage(decor.WCSyncWidth),
				),
				mpb.AppendDecorators(
					decor.EwmaETA(decor.ET_STYLE_GO, 60, decor.WCSyncWidth), // 估算剩余时间
				),
			)
		} else {
			// 总量未知：从 0 开始，按批动态增加总量
			bar = p.AddBar(
				0,
				mpb.PrependDecorators(
					decor.Name("["+cfg.Table+"] "),
					decor.CountersNoUnit("%d/%d"),
					decor.Percentage(decor.WCSyncWidth),
				),
				mpb.AppendDecorators(
					decor.EwmaETA(decor.ET_STYLE_GO, 60, decor.WCSyncWidth),
				),
			)
		}
	}

	// RPS 节流器
	var rate <-chan time.Time
	if cfg.RPS > 0 {
		interval := time.Second / time.Duration(cfg.RPS)
		if interval <= 0 {
			interval = time.Millisecond
		}
		tk := time.NewTicker(interval)
		defer tk.Stop()
		rate = tk.C
	}

	if len(cfg.PK) > 0 {
		return processWithPK(db, cfg, rate, bar, total)
	}
	return processNoPK(db, cfg, rate, bar, total)
}

// 统计表总行数
func countTotalRows(db *sql.DB, table string) (int64, error) {
	var total int64
	row := db.QueryRow("SELECT COUNT(*) FROM `" + table + "`")
	if err := row.Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// ---------- 复合主键/单主键 增量遍历 ----------
func processWithPK(db *sql.DB, cfg MySQLConfig, rate <-chan time.Time, bar *mpb.Bar, total int64) error {
	log.Printf("[mysql] 开始处理（有主键） table=%s pk=%v cols=%v", cfg.Table, cfg.PK, cfg.Columns)

	lastKey := make([]sql.NullString, len(cfg.PK)) // 初始为空
	cols := append([]string{}, cfg.PK...)
	cols = append(cols, cfg.Columns...)
	quoted := quoteAll(cols)

	for {
		// SELECT
		selectSQL := fmt.Sprintf("SELECT %s FROM `%s`", strings.Join(quoted, ","), cfg.Table)
		args := []interface{}{}
		if anyValid(lastKey) {
			ph := make([]string, len(cfg.PK))
			for i := range ph {
				ph[i] = "?"
				args = append(args, nz(lastKey[i]))
			}
			selectSQL += fmt.Sprintf(" WHERE (%s) > (%s)", strings.Join(cfg.PK, ","), strings.Join(ph, ","))
		}
		selectSQL += fmt.Sprintf(" ORDER BY %s LIMIT ?", strings.Join(cfg.PK, ","))
		args = append(args, cfg.BatchSize)

		rows, err := db.Query(selectSQL, args...)
		if err != nil {
			log.Printf("[mysql] query err: %v, 5s 后重试…", err)
			time.Sleep(5 * time.Second)
			continue
		}

		n := 0
		type row struct {
			pk   []sql.NullString
			data map[string]*string
		}
		var batch []row

		for rows.Next() {
			dst := make([]interface{}, len(cols))
			for i := 0; i < len(cols); i++ {
				var ns sql.NullString
				dst[i] = &ns
			}
			if err := rows.Scan(dst...); err != nil {
				log.Printf("[mysql] scan err: %v", err)
				continue
			}
			r := row{pk: make([]sql.NullString, len(cfg.PK)), data: map[string]*string{}}
			for i := range cfg.PK {
				r.pk[i] = *dst[i].(*sql.NullString)
			}
			for i, c := range cfg.Columns {
				ns := *dst[len(cfg.PK)+i].(*sql.NullString)
				if ns.Valid {
					v := ns.String
					r.data[c] = &v
				} else {
					r.data[c] = nil
				}
			}
			batch = append(batch, r)
			n++
		}
		rows.Close()

		if n == 0 {
			if bar != nil {
				// 补齐并标记完成
				bar.SetTotal(bar.Current(), true)
			}
			log.Println("[mysql] 处理完成（无更多数据）")
			return nil
		}

		// 未知总量：按批动态扩充总量
		if bar != nil && total <= 0 {
			bar.SetTotal(bar.Current()+int64(n), false)
		}

		// 逐行处理
		for _, r := range batch {
			if rate != nil {
				<-rate
			}

			changed := map[string]string{}
			for _, c := range cfg.Columns {
				ptr := r.data[c]
				if ptr == nil || *ptr == "" {
					continue
				}
				out, need, err := ConvertIfNeeded(cfg.To, *ptr)
				if err != nil {
					log.Printf("[mysql] convert err: %v", err)
					continue
				}
				if need {
					changed[c] = out
				}
			}

			if len(changed) > 0 && !cfg.DryRun {
				// UPDATE SET … WHERE pk1=? AND pk2=? …
				setParts := []string{}
				args := []interface{}{}
				for _, c := range cfg.Columns {
					if v, ok := changed[c]; ok {
						setParts = append(setParts, fmt.Sprintf("`%s` = ?", c))
						args = append(args, v)
					}
				}
				where := []string{}
				for i, pk := range cfg.PK {
					if r.pk[i].Valid {
						where = append(where, fmt.Sprintf("`%s` = ?", pk))
						args = append(args, r.pk[i].String)
					} else {
						where = append(where, fmt.Sprintf("`%s` IS NULL", pk))
					}
				}
				sqlText := fmt.Sprintf("UPDATE `%s` SET %s WHERE %s", cfg.Table, strings.Join(setParts, ","), strings.Join(where, " AND "))

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, err := db.ExecContext(ctx, sqlText, args...)
				cancel()
				if err != nil {
					log.Printf("[mysql] update err: %v -- sql=%s -- args=%v", err, sqlText, args)
				}
			}

			// 推进进度（行）
			if bar != nil {
				bar.EwmaIncrement(1)
			}
		}

		// 记录 lastKey：取本批最后一行的主键值
		last := batch[len(batch)-1]
		for i := range cfg.PK {
			lastKey[i] = last.pk[i]
		}
	}
}

// ---------- 无主键表：使用 identify-by 或整行匹配 ----------
func processNoPK(db *sql.DB, cfg MySQLConfig, rate <-chan time.Time, bar *mpb.Bar, total int64) error {
	log.Printf("[mysql] 开始处理（无主键） table=%s cols=%v identifyBy=%v", cfg.Table, cfg.Columns, cfg.IdentifyBy)

	// 读取所有列名
	allCols, err := getAllColumns(db, cfg.Table)
	if err != nil {
		return fmt.Errorf("获取列失败：%w", err)
	}
	if len(allCols) == 0 {
		return fmt.Errorf("表 %s 无列", cfg.Table)
	}

	offset := 0
	for {
		selectSQL := fmt.Sprintf("SELECT %s FROM `%s` LIMIT ? OFFSET ?", strings.Join(quoteAll(allCols), ","), cfg.Table)
		rows, err := db.Query(selectSQL, cfg.BatchSize, offset)
		if err != nil {
			log.Printf("[mysql] query err: %v, 5s 后重试…", err)
			time.Sleep(5 * time.Second)
			continue
		}
		n := 0

		// 批内处理
		type rowValsT = []*string
		var rowVals rowValsT

		for rows.Next() {
			dst := make([]interface{}, len(allCols))
			rowVals = make([]*string, len(allCols))
			for i := 0; i < len(allCols); i++ {
				var ns sql.NullString
				dst[i] = &ns
			}
			if err := rows.Scan(dst...); err != nil {
				log.Printf("[mysql] scan err: %v", err)
				continue
			}
			for i := 0; i < len(allCols); i++ {
				ns := *dst[i].(*sql.NullString)
				if ns.Valid {
					v := ns.String
					rowVals[i] = &v
				} else {
					rowVals[i] = nil
				}
			}

			// 组装需要转换的列
			changed := map[string]string{}
			for _, c := range cfg.Columns {
				idx := indexOf(allCols, c)
				if idx < 0 || rowVals[idx] == nil || *rowVals[idx] == "" {
					continue
				}
				if rate != nil {
					<-rate
				}
				out, need, err := ConvertIfNeeded(cfg.To, *rowVals[idx])
				if err != nil {
					log.Printf("[mysql] convert err: %v", err)
					continue
				}
				if need {
					changed[c] = out
				}
			}

			if len(changed) > 0 && !cfg.DryRun {
				// 构建 WHERE（优先 identify-by）
				where := []string{}
				args := []interface{}{}

				setParts := []string{}
				for _, c := range cfg.Columns {
					if v, ok := changed[c]; ok {
						setParts = append(setParts, fmt.Sprintf("`%s` = ?", c))
						args = append(args, v)
					}
				}

				if len(cfg.IdentifyBy) > 0 {
					for _, col := range cfg.IdentifyBy {
						idx := indexOf(allCols, col)
						if idx < 0 {
							continue
						}
						if rowVals[idx] == nil {
							where = append(where, fmt.Sprintf("`%s` IS NULL", col))
						} else {
							where = append(where, fmt.Sprintf("`%s` = ?", col))
							args = append(args, *rowVals[idx])
						}
					}
				} else {
					// 整行匹配（可能较慢），并加 LIMIT 1
					for i, col := range allCols {
						if rowVals[i] == nil {
							where = append(where, fmt.Sprintf("`%s` IS NULL", col))
						} else {
							where = append(where, fmt.Sprintf("`%s` = ?", col))
							args = append(args, *rowVals[i])
						}
					}
				}

				sqlText := fmt.Sprintf("UPDATE `%s` SET %s WHERE %s", cfg.Table, strings.Join(setParts, ","), strings.Join(where, " AND "))
				if len(cfg.IdentifyBy) == 0 {
					sqlText += " LIMIT 1"
				}

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, err := db.ExecContext(ctx, sqlText, args...)
				cancel()
				if err != nil {
					log.Printf("[mysql] update err: %v -- sql=%s -- args=%v", err, sqlText, args)
				}
			}

			// 推进进度（行）
			if bar != nil {
				bar.EwmaIncrement(1)
			}

			n++
		}
		rows.Close()

		if n == 0 {
			if bar != nil {
				bar.SetTotal(bar.Current(), true)
			}
			log.Println("[mysql] 处理完成（无更多数据）")
			return nil
		}

		// 未知总量：按批动态扩充总量
		if bar != nil && total <= 0 {
			bar.SetTotal(bar.Current()+int64(n), false)
		}

		offset += n
	}
}

func getAllColumns(db *sql.DB, table string) ([]string, error) {
	q := `SELECT COLUMN_NAME FROM information_schema.columns 
	      WHERE table_schema = DATABASE() AND table_name = ? 
		  ORDER BY ORDINAL_POSITION`
	rows, err := db.Query(q, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, nil
}

func quoteAll(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = fmt.Sprintf("`%s`", c)
	}
	return out
}

func indexOf(arr []string, s string) int {
	for i, v := range arr {
		if v == s {
			return i
		}
	}
	return -1
}

func anyValid(keys []sql.NullString) bool {
	for _, k := range keys {
		if k.Valid {
			return true
		}
	}
	return false
}

func nz(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
