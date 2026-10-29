package datasource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	mssql "github.com/microsoft/go-mssqldb"
)

// hostPort adds the default port of the kind when the address has none.
func hostPort(addr, port string) string {
	addr = strings.TrimSpace(addr)
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(strings.Trim(addr, "[]"), port)
}

// openSQL opens one connection to a SQL data source.
func openSQL(c Config) (*sql.DB, error) {
	if strings.TrimSpace(c.Address) == "" {
		return nil, errors.New("the address is empty")
	}
	var db *sql.DB
	switch c.Kind {
	case Postgres:
		mode := map[string]string{"": "prefer", "require": "require", "verify": "verify-full", "disable": "disable"}[c.TLS]
		if mode == "" {
			mode = "prefer"
		}
		u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, c.Secret), Host: hostPort(c.Address, "5432"), Path: "/" + c.Database,
			RawQuery: url.Values{"sslmode": {mode}, "connect_timeout": {"10"}, "application_name": {"WatchZit"}}.Encode()}
		cfg, err := pgx.ParseConfig(u.String())
		if err != nil {
			return nil, errors.New("invalid PostgreSQL address")
		}
		db = stdlib.OpenDB(*cfg)
	case MySQL:
		cfg := mysql.NewConfig()
		cfg.Net, cfg.Addr, cfg.User, cfg.Passwd, cfg.DBName = "tcp", hostPort(c.Address, "3306"), c.User, c.Secret, c.Database
		cfg.ParseTime, cfg.Loc, cfg.Timeout = true, time.UTC, 10*time.Second
		cfg.Params = map[string]string{"time_zone": "'+00:00'"}
		cfg.TLSConfig = map[string]string{"": "preferred", "require": "skip-verify", "verify": "true", "disable": "false"}[c.TLS]
		if cfg.TLSConfig == "" {
			cfg.TLSConfig = "preferred"
		}
		conn, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, err
		}
		db = sql.OpenDB(conn)
	case MSSQL:
		q := url.Values{"app name": {"WatchZit"}, "dial timeout": {"10"}}
		if c.Database != "" {
			q.Set("database", c.Database)
		}
		switch c.TLS {
		case "verify":
			q.Set("encrypt", "true")
		case "disable":
			q.Set("encrypt", "disable")
		default:
			q.Set("encrypt", "true")
			q.Set("TrustServerCertificate", "true")
		}
		u := url.URL{Scheme: "sqlserver", User: url.UserPassword(c.User, c.Secret), Host: hostPort(c.Address, "1433"), RawQuery: q.Encode()}
		conn, err := mssql.NewConnector(u.String())
		if err != nil {
			return nil, errors.New("invalid SQL Server address")
		}
		db = sql.OpenDB(conn)
	default:
		return nil, ErrKind
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(time.Minute)
	return db, nil
}

func testSQL(ctx context.Context, c Config) error {
	db, err := openSQL(c)
	if err != nil {
		return err
	}
	defer db.Close()
	var one int
	return db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

var (
	reTimeFilter = regexp.MustCompile(`\$__timeFilter\(\s*([^)]+?)\s*\)`)
	reTimeGroup  = regexp.MustCompile(`\$__timeGroup\(\s*([^),]+?)\s*(?:,\s*([0-9]+)\s*)?\)`)
)

// Expand replaces the time macros of a SQL query:
//
//	$__timeFilter(col)  col between the start and end of the range
//	$__timeGroup(col)   col rounded down to the graph interval (or ", seconds")
//	$__timeFrom, $__timeTo, $__unixFrom, $__unixTo, $__interval_s
func Expand(kind, q string, r Request) string {
	from, to := r.From.UTC(), r.To.UTC()
	step := int64(r.Step() / time.Second)
	lit := func(t time.Time) string {
		switch kind {
		case Postgres:
			return "'" + t.Format("2006-01-02 15:04:05") + "+00'::timestamptz"
		case MSSQL:
			return "CAST('" + t.Format("2006-01-02T15:04:05") + "' AS datetime2)"
		}
		return "'" + t.Format("2006-01-02 15:04:05") + "'"
	}
	group := func(col string, n int64) string {
		s := strconv.FormatInt(n, 10)
		switch kind {
		case Postgres:
			return "to_timestamp(floor(extract(epoch from " + col + ")/" + s + ")*" + s + ")"
		case MSSQL:
			return "DATEADD(second, (DATEDIFF_BIG(second, '1970-01-01', " + col + ")/" + s + ")*" + s + ", CAST('1970-01-01' AS datetime2))"
		}
		return "FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(" + col + ")/" + s + ")*" + s + ")"
	}
	q = reTimeFilter.ReplaceAllStringFunc(q, func(m string) string {
		col := reTimeFilter.FindStringSubmatch(m)[1]
		return "(" + col + " BETWEEN " + lit(from) + " AND " + lit(to) + ")"
	})
	q = reTimeGroup.ReplaceAllStringFunc(q, func(m string) string {
		sm := reTimeGroup.FindStringSubmatch(m)
		n := step
		if v, err := strconv.ParseInt(sm[2], 10, 64); err == nil && v > 0 {
			n = v
		}
		return group(sm[1], n)
	})
	return strings.NewReplacer(
		"$__timeFrom", lit(from), "$__timeTo", lit(to),
		"$__unixFrom", strconv.FormatInt(from.Unix(), 10), "$__unixTo", strconv.FormatInt(to.Unix(), 10),
		"$__interval_s", strconv.FormatInt(step, 10),
	).Replace(q)
}

// querySQL runs a query in a read-only transaction where the database has
// one. Its rows: a time column ("time", or the first one that holds times),
// numeric columns (one series each) and optionally a text column that names
// the series ("metric", or the first text one), as in long tables.
func querySQL(ctx context.Context, c Config, r Request) ([]Series, error) {
	db, err := openSQL(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	q := Expand(c.Kind, r.Query, r)
	var rows *sql.Rows
	if c.Kind == MSSQL { // the driver has no read-only transactions: use a read-only user
		rows, err = db.QueryContext(ctx, q)
	} else {
		tx, terr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if terr != nil {
			return nil, terr
		}
		defer func() { _ = tx.Rollback() }()
		rows, err = tx.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var table [][]any
	for rows.Next() {
		if len(table) >= MaxRows {
			return nil, fmt.Errorf("the query gives more than %d rows; group it by time", MaxRows)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		table = append(table, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tableSeries(cols, table)
}

// tableSeries turns rows into series (see querySQL).
func tableSeries(cols []string, table [][]any) ([]Series, error) {
	if len(table) == 0 {
		return []Series{}, nil
	}
	const (
		unknown = iota
		isTime
		isNum
		isText
	)
	kind := make([]int, len(cols))
	for i := range cols {
		for _, row := range table {
			v := row[i]
			if v == nil {
				continue
			}
			switch x := v.(type) {
			case time.Time:
				kind[i] = isTime
			case string, []byte:
				s := x
				if b, ok := x.([]byte); ok {
					s = string(b)
				}
				if _, ok := num(s); ok {
					kind[i] = isNum
				} else {
					kind[i] = isText
				}
			default:
				if _, ok := num(v); ok {
					kind[i] = isNum
				} else {
					kind[i] = isText
				}
			}
			break
		}
	}
	tc, mc := -1, -1
	for i, n := range cols {
		if strings.EqualFold(n, "time") || strings.EqualFold(n, "time_sec") {
			tc = i
			break
		}
	}
	if tc < 0 {
		for i := range cols {
			if kind[i] == isTime {
				tc = i
				break
			}
		}
	}
	if tc < 0 {
		return nil, errors.New("the query gives no time column (name one time)")
	}
	for i, n := range cols {
		if i != tc && strings.EqualFold(n, "metric") {
			mc = i
		}
	}
	if mc < 0 {
		for i := range cols {
			if i != tc && kind[i] == isText {
				mc = i
				break
			}
		}
	}
	var vcs []int
	for i := range cols {
		if i != tc && i != mc && kind[i] != isText {
			vcs = append(vcs, i)
		}
	}
	if len(vcs) == 0 {
		return nil, errors.New("the query gives no numeric column")
	}
	idx := map[string]int{}
	var out []Series
	for _, row := range table {
		t, ok := unixOf(row[tc])
		if !ok {
			continue
		}
		prefix := ""
		if mc >= 0 {
			prefix = fmt.Sprint(row[mc])
			if b, ok := row[mc].([]byte); ok {
				prefix = string(b)
			}
		}
		for _, vc := range vcs {
			name := cols[vc]
			if mc >= 0 {
				name = prefix
				if len(vcs) > 1 {
					name += " " + cols[vc]
				}
			}
			k, seen := idx[name]
			if !seen {
				if len(out) >= MaxSeries {
					continue
				}
				k = len(out)
				idx[name] = k
				out = append(out, Series{Name: name})
			}
			v, _ := num(row[vc])
			out[k].T = append(out[k].T, t)
			out[k].V = append(out[k].V, v)
		}
	}
	return out, nil
}
