module github.com/neurekadev/dockyard

go 1.27

toolchain go1.27.1

require (
	github.com/coder/websocket v1.8.15
	github.com/danielgtaylor/huma/v2 v2.39.1
	github.com/google/uuid v1.6.0
	github.com/uptrace/bun v1.2.18
	github.com/uptrace/bun/dialect/sqlitedialect v1.2.18
	github.com/uptrace/bun/driver/sqliteshim v1.2.18
	golang.org/x/crypto v0.57.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/mattn/go-isatty v0.0.23 // indirect
	github.com/mattn/go-sqlite3 v1.14.34 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/puzpuzpuz/xsync/v3 v3.5.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tmthrgd/go-hex v0.0.0-20190904060850-447a3041c3bc // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/exp v0.0.0-20260218203240-3dfff04db8fa // indirect
	golang.org/x/sys v0.48.0 // indirect
	modernc.org/libc v1.68.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.46.1 // indirect
)

ignore ./web/node_modules
