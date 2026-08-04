package migrations

import (
	_ "embed"
)

//go:embed 000001_init.up.sql
var InitSQL string
