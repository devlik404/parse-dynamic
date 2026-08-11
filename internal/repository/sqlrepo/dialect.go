package sqlrepo

import (
	"fmt"
	"regexp"
	"strings"
)

type dialectName string

const (
	dialectPostgres  dialectName = "postgres"
	dialectMySQL     dialectName = "mysql"
	dialectSQLServer dialectName = "sqlserver"
)

type dialect struct {
	name                dialectName
	driverName          string
	maxParameters       int
	maxRowsPerStatement int
}

var sqlIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func resolveDialect(driver string) (dialect, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "pgx", "postgres", "postgresql":
		return dialect{
			name:          dialectPostgres,
			driverName:    "pgx",
			maxParameters: 65_535,
		}, nil
	case "mysql", "mariadb":
		return dialect{
			name:          dialectMySQL,
			driverName:    "mysql",
			maxParameters: 65_535,
		}, nil
	case "sqlserver", "mssql":
		return dialect{
			name:                dialectSQLServer,
			driverName:          "sqlserver",
			maxParameters:       2_100,
			maxRowsPerStatement: 1_000,
		}, nil
	default:
		return dialect{}, fmt.Errorf("unsupported DB_DRIVER %q (supported: postgres, mysql, sqlserver)", driver)
	}
}

func (d dialect) quoteIdentifier(identifier string) (string, error) {
	if !sqlIdentifierPattern.MatchString(identifier) {
		return "", fmt.Errorf("unsafe SQL identifier %q", identifier)
	}

	switch d.name {
	case dialectMySQL:
		return "`" + identifier + "`", nil
	case dialectSQLServer:
		return "[" + identifier + "]", nil
	case dialectPostgres:
		return `"` + identifier + `"`, nil
	default:
		return "", fmt.Errorf("unsupported SQL dialect %q", d.name)
	}
}

func (d dialect) placeholder(position int) string {
	switch d.name {
	case dialectPostgres:
		return fmt.Sprintf("$%d", position)
	case dialectSQLServer:
		return fmt.Sprintf("@p%d", position)
	default:
		return "?"
	}
}
