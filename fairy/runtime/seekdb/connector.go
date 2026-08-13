package seekdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

const (
	connectionMaxLifetime = 30 * time.Minute
	connectionMaxIdleTime = 5 * time.Minute
)

func openSQLDatabase(config Config) (*sql.DB, error) {
	return openSQLDatabaseForName(config, config.Database)
}

func openSQLDatabaseForName(config Config, databaseName string) (*sql.DB, error) {
	connector, err := mysqlConnectorForName(config, databaseName)
	if err != nil {
		return nil, redactRuntimeError(config, err)
	}
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(config.MaxOpenConns)
	database.SetMaxIdleConns(config.MaxIdleConns)
	database.SetConnMaxLifetime(connectionMaxLifetime)
	database.SetConnMaxIdleTime(connectionMaxIdleTime)
	return database, nil
}

func mysqlConnector(config Config) (driver.Connector, error) {
	return mysqlConnectorForName(config, config.Database)
}

func mysqlConnectorForName(config Config, databaseName string) (driver.Connector, error) {
	driverConfig, err := mysqlDriverConfigForName(config, databaseName)
	if err != nil {
		return nil, err
	}
	connector, err := mysql.NewConnector(driverConfig)
	if err != nil {
		return nil, err
	}
	return connector, nil
}

func mysqlDriverConfig(config Config) (*mysql.Config, error) {
	return mysqlDriverConfigForName(config, config.Database)
}

func mysqlDriverConfigForName(config Config, databaseName string) (*mysql.Config, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if databaseName != "" && !identifierPattern.MatchString(databaseName) {
		return nil, errors.New("SeekDB SQL database name must be a portable identifier")
	}
	driverConfig := mysql.NewConfig()
	driverConfig.User = config.User
	driverConfig.Passwd = config.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = config.Address
	driverConfig.DBName = databaseName
	driverConfig.Loc = time.UTC
	driverConfig.Timeout = config.ConnectLimit
	driverConfig.ReadTimeout = config.QueryLimit
	driverConfig.WriteTimeout = config.QueryLimit
	driverConfig.ParseTime = true
	driverConfig.CheckConnLiveness = true
	driverConfig.AllowNativePasswords = true
	driverConfig.AllowAllFiles = false
	driverConfig.AllowCleartextPasswords = false
	driverConfig.AllowFallbackToPlaintext = false
	driverConfig.AllowOldPasswords = false
	driverConfig.InterpolateParams = false
	driverConfig.MultiStatements = false
	driverConfig.Logger = discardDriverLogger{}
	return driverConfig, nil
}

func probeSQL(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("SeekDB SQL connection is not open")
	}
	return database.PingContext(ctx)
}

type discardDriverLogger struct{}

func (discardDriverLogger) Print(...any) {}
