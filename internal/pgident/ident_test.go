package pgident

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantSchema string
		wantTable  string
		wantErr    string
	}{
		{
			name:       "lowercase schema-qualified",
			ref:        "public.orders",
			wantSchema: "public",
			wantTable:  "orders",
		},
		{
			name:       "unqualified",
			ref:        "orders",
			wantSchema: "",
			wantTable:  "orders",
		},
		{
			name:       "mixed-case quoted table",
			ref:        `public."CamelCaseTable"`,
			wantSchema: "public",
			wantTable:  "CamelCaseTable",
		},
		{
			name:       "quoted unqualified",
			ref:        `"CamelCaseTable"`,
			wantSchema: "",
			wantTable:  "CamelCaseTable",
		},
		{
			name:       "both quoted mixed-case",
			ref:        `"Sales"."Orders"`,
			wantSchema: "Sales",
			wantTable:  "Orders",
		},
		{
			name:       "dot inside quoted table",
			ref:        `public."weird.table"`,
			wantSchema: "public",
			wantTable:  "weird.table",
		},
		{
			name:       "escaped quote inside identifier",
			ref:        `public."say""hello"`,
			wantSchema: "public",
			wantTable:  `say"hello`,
		},
		{
			name:       "whitespace trimmed",
			ref:        `  public."CamelCaseTable"  `,
			wantSchema: "public",
			wantTable:  "CamelCaseTable",
		},
		{
			name:    "empty",
			ref:     "",
			wantErr: "empty table reference",
		},
		{
			name:    "empty quoted identifier",
			ref:     `""`,
			wantErr: "empty identifier",
		},
		{
			name:    "empty quoted schema component",
			ref:     `"".orders`,
			wantErr: "empty identifier",
		},
		{
			name:    "empty quoted table component",
			ref:     `public.""`,
			wantErr: "empty identifier",
		},
		{
			name:    "unclosed quote",
			ref:     `public."CamelCase`,
			wantErr: "unclosed quote",
		},
		{
			name:    "trailing dot",
			ref:     "public.",
			wantErr: "trailing dot",
		},
		{
			name:    "too many components without quotes",
			ref:     "a.b.c",
			wantErr: "too many dotted components",
		},
		{
			name:    "quote mid unquoted ident",
			ref:     `pub"lic.orders`,
			wantErr: "quote mid-identifier",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema, table, err := Parse(tt.ref)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSchema, schema)
			assert.Equal(t, tt.wantTable, table)
		})
	}
}

func TestQualify(t *testing.T) {
	assert.Equal(t, `"public"."CamelCaseTable"`, Qualify("public", "CamelCaseTable"))
	assert.Equal(t, `"Orders"`, Qualify("", "Orders"))
	assert.Equal(t, `"Sales"."Orders"`, Qualify("Sales", "Orders"))

	// Round-trip: Parse then Qualify must not triple-quote.
	schema, table, err := Parse(`public."CamelCaseTable"`)
	require.NoError(t, err)
	assert.Equal(t, `"public"."CamelCaseTable"`, Qualify(schema, table))
	assert.NotContains(t, Qualify(schema, table), `"""`)
}
