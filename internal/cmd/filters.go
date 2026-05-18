package cmd

import (
	"fmt"
	"strings"

	"github.com/olucasandrade/kaptanto/internal/backfill"
	"github.com/olucasandrade/kaptanto/internal/config"
	"github.com/olucasandrade/kaptanto/internal/output"
)

func buildTableFilters(tables map[string]config.TableConfig) (
	map[string]*output.RowFilter,
	map[string][]string,
	error,
) {
	if len(tables) == 0 {
		return nil, nil, nil
	}
	rowFilters := make(map[string]*output.RowFilter, len(tables))
	colFilters := make(map[string][]string, len(tables))
	for table, tc := range tables {
		rf, err := output.ParseRowFilter(tc.Where)
		if err != nil {
			return nil, nil, fmt.Errorf("table %q where filter: %w", table, err)
		}
		rowFilters[table] = rf
		if len(tc.Columns) > 0 {
			colFilters[table] = tc.Columns
		}
	}
	return rowFilters, colFilters, nil
}

func buildBackfillConfigs(tables map[string]config.TableConfig, sourceID string) []backfill.BackfillConfig {
	configs := make([]backfill.BackfillConfig, 0, len(tables))
	for tableKey := range tables {
		schema, table := "", tableKey
		if parts := strings.SplitN(tableKey, ".", 2); len(parts) == 2 {
			schema, table = parts[0], parts[1]
		}
		configs = append(configs, backfill.BackfillConfig{
			SourceID:      sourceID,
			Schema:        schema,
			Table:         table,
			Strategy:      "snapshot_and_stream",
			PKCols:        []string{"id"},
			NumPartitions: numEventLogPartitions,
		})
	}
	return configs
}
