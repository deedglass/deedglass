// Package witness is a stub core package with a planted vendor import.
package witness

import (
	_ "github.com/deedglass/deedglass/internal/connector"
	_ "github.com/deedglass/deedglass/internal/connector/postgres"
)
