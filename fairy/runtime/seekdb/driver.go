package seekdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	connectionMaxLifetime = 30 * time.Minute
	connectionMaxIdleTime = 5 * time.Minute
)

func openSQLDatabase(config Config) (*sql.DB, error) {
	return openSQLDatabaseForName(config, config.Database)
}

func openSQLDatabaseForName(config Config, databaseName string) (*sql.DB, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if databaseName != "" && !identifierPattern.MatchString(databaseName) {
		return nil, errors.New("SeekDB SQL database name must be a portable identifier")
	}
	database := sql.OpenDB(embedConnector{database: databaseName})
	applyPool(database, config)
	return database, nil
}

func applyPool(database *sql.DB, config Config) {
	database.SetMaxOpenConns(config.MaxOpenConns)
	database.SetMaxIdleConns(config.MaxIdleConns)
	database.SetConnMaxLifetime(connectionMaxLifetime)
	database.SetConnMaxIdleTime(connectionMaxIdleTime)
}

func probeSQL(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("SeekDB SQL connection is not open")
	}
	return database.PingContext(ctx)
}

type embedConnector struct {
	database string
}

func (c embedConnector) Connect(context.Context) (driver.Conn, error) {
	handle, err := engineConnect(c.database, true)
	if err != nil {
		return nil, err
	}
	return &embedConn{handle: handle}, nil
}

func (c embedConnector) Driver() driver.Driver { return embedDriver{} }

type embedDriver struct{}

func (embedDriver) Open(name string) (driver.Conn, error) {
	return embedConnector{database: name}.Connect(context.Background())
}

type embedConn struct {
	mu     sync.Mutex
	handle *engineHandle
	closed bool
}

func (c *embedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("SeekDB prepared statements are interpolated by Exec/Query")
}

func (c *embedConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.handle.Close()
	c.closed = true
	return nil
}

func (c *embedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *embedConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.handle.Begin(); err != nil {
		return nil, err
	}
	return &embedTx{conn: c}, nil
}

func (c *embedConn) Ping(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handle.Ping()
}

func (c *embedConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sqlText, err := interpolateSQL(rewriteEmbedCharsets(query), args)
	if err != nil {
		return nil, err
	}
	result, err := c.handle.Query(sqlText)
	if err != nil {
		return nil, err
	}
	if result != nil {
		resultFree(result)
	}
	return embedResult{affected: c.handle.Affected(), lastID: c.handle.InsertID()}, nil
}

func (c *embedConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sqlText, err := interpolateSQL(rewriteEmbedCharsets(query), args)
	if err != nil {
		return nil, err
	}
	result, err := c.handle.Query(sqlText)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return &embedRows{closed: true}, nil
	}
	return &embedRows{result: result, columns: resultColumnNames(result)}, nil
}

type embedTx struct {
	conn *embedConn
}

func (t *embedTx) Commit() error {
	t.conn.mu.Lock()
	defer t.conn.mu.Unlock()
	return t.conn.handle.Commit()
}

func (t *embedTx) Rollback() error {
	t.conn.mu.Lock()
	defer t.conn.mu.Unlock()
	return t.conn.handle.Rollback()
}

type embedResult struct {
	affected int64
	lastID   int64
}

func (r embedResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r embedResult) RowsAffected() (int64, error) { return r.affected, nil }

type embedRows struct {
	result  resultHandle
	columns []string
	closed  bool
}

func (r *embedRows) Columns() []string {
	return r.columns
}

func (r *embedRows) Close() error {
	if r.closed {
		return nil
	}
	if r.result != nil {
		resultFree(r.result)
	}
	r.closed = true
	return nil
}

func (r *embedRows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}
	row, ok := resultFetch(r.result)
	if !ok {
		return io.EOF
	}
	for i := range dest {
		value, isNull, err := rowValue(row, i)
		if err != nil {
			return err
		}
		if isNull {
			dest[i] = nil
			continue
		}
		dest[i] = value
	}
	return nil
}
