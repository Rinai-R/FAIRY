package seekdb

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func interpolateSQL(query string, args []driver.NamedValue) (string, error) {
	var builder strings.Builder
	builder.Grow(len(query))
	argIndex := 0
	for i := 0; i < len(query); i++ {
		switch query[i] {
		case '\'', '"', '`':
			end := scanSQLQuoted(query, i)
			builder.WriteString(query[i:end])
			i = end - 1
		case '?':
			if argIndex >= len(args) {
				return "", errorsNew("SeekDB query is missing arguments")
			}
			literal, err := sqlLiteral(args[argIndex].Value)
			if err != nil {
				return "", err
			}
			builder.WriteString(literal)
			argIndex++
		default:
			builder.WriteByte(query[i])
		}
	}
	if argIndex != len(args) {
		return "", errorsNew("SeekDB query has more arguments than placeholders")
	}
	return builder.String(), nil
}

func scanSQLQuoted(query string, start int) int {
	quote := query[start]
	for i := start + 1; i < len(query); i++ {
		if query[i] == '\\' && i+1 < len(query) {
			i++
			continue
		}
		if query[i] != quote {
			continue
		}
		if i+1 < len(query) && query[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(query)
}

func sqlLiteral(value driver.Value) (string, error) {
	if value == nil {
		return "NULL", nil
	}
	switch typed := value.(type) {
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case bool:
		if typed {
			return "1", nil
		}
		return "0", nil
	case string:
		return quoteBytes([]byte(typed)), nil
	case []byte:
		return quoteBytes(typed), nil
	case time.Time:
		return "'" + typed.UTC().Format("2006-01-02 15:04:05.000000") + "'", nil
	default:
		return "", fmt.Errorf("unsupported SeekDB argument type %T", value)
	}
}

func quoteBytes(value []byte) string {
	var builder strings.Builder
	builder.Grow(len(value) + 2)
	builder.WriteByte('\'')
	for _, b := range value {
		switch b {
		case 0:
			builder.WriteString(`\0`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\\':
			builder.WriteString(`\\`)
		case '\'':
			builder.WriteString(`\'`)
		case '"':
			builder.WriteString(`\"`)
		case 26:
			builder.WriteString(`\Z`)
		default:
			builder.WriteByte(b)
		}
	}
	builder.WriteByte('\'')
	return builder.String()
}

func errorsNew(message string) error {
	return &Error{Message: message}
}
