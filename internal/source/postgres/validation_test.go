package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitSchemaTable(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantSchema string
		wantTable  string
		wantErr    string
	}{
		{
			name:       "schema-qualified lowercase",
			in:         "public.orders",
			wantSchema: "public",
			wantTable:  "orders",
		},
		{
			name:       "unqualified defaults to public",
			in:         "orders",
			wantSchema: "public",
			wantTable:  "orders",
		},
		{
			name:       "quoted mixed-case strips quotes",
			in:         `public."CamelCaseTable"`,
			wantSchema: "public",
			wantTable:  "CamelCaseTable",
		},
		{
			name:       "quoted unqualified defaults to public",
			in:         `"CamelCaseTable"`,
			wantSchema: "public",
			wantTable:  "CamelCaseTable",
		},
		{
			name:    "unclosed quote",
			in:      `public."Camel`,
			wantErr: "unclosed quote",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema, table, err := splitSchemaTable(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSchema, schema)
			assert.Equal(t, tt.wantTable, table)
			assert.NotContains(t, table, `"`)
		})
	}
}
