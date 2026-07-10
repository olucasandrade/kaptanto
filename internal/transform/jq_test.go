package transform

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const sampleJQRaw = `{"id":1,"status":"open","table":"orders","n":42}`

func TestJQGolden(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
		raw        string
		wantOut    string
		wantDrop   bool
		wantErr    bool
		errSubstr  string
	}{
		{
			name:       "null drop",
			expression: "null",
			raw:        sampleJQRaw,
			wantDrop:   true,
		},
		{
			name:       "zero outputs drop",
			expression: "empty",
			raw:        sampleJQRaw,
			wantDrop:   true,
		},
		{
			name:       "select false drop",
			expression: "select(false)",
			raw:        sampleJQRaw,
			wantDrop:   true,
		},
		{
			name:       "string passthrough",
			expression: ".status",
			raw:        sampleJQRaw,
			wantOut:    `"open"`,
		},
		{
			name:       "number passthrough",
			expression: ".n",
			raw:        sampleJQRaw,
			wantOut:    "42",
		},
		{
			name:       "object passthrough",
			expression: "{id, status}",
			raw:        sampleJQRaw,
			wantOut:    `{"id":1,"status":"open"}`,
		},
		{
			name:       "error boom",
			expression: `error("boom")`,
			raw:        sampleJQRaw,
			wantErr:    true,
			errSubstr:  "boom",
		},
		{
			name:       "type error",
			expression: "1 + .status",
			raw:        sampleJQRaw,
			wantErr:    true,
		},
		{
			name:       "two outputs",
			expression: "1, 2",
			raw:        sampleJQRaw,
			wantErr:    true,
			errSubstr:  "produced 2 outputs",
		},
		{
			name:       "array stream two outputs",
			expression: ".[]",
			raw:        `[1,2]`,
			wantErr:    true,
			errSubstr:  "produced 2 outputs",
		},
		{
			name:       "repeat stops quickly",
			expression: "repeat(1)",
			raw:        sampleJQRaw,
			wantErr:    true,
			errSubstr:  "produced 2 outputs",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eng, err := Compile(LangJQ, tt.expression)
			require.NoError(t, err)

			start := time.Now()
			out, drop, err := eng.Apply([]byte(tt.raw), nil)
			elapsed := time.Since(start)

			if tt.name == "repeat stops quickly" {
				require.Less(t, elapsed, 500*time.Millisecond, "repeat(1) must not hang")
			}

			if tt.wantErr {
				require.Error(t, err)
				var re *RuntimeError
				require.ErrorAs(t, err, &re)
				require.Equal(t, LangJQ, re.Language)
				require.NotNil(t, re.Cause)
				if tt.errSubstr != "" {
					require.Contains(t, err.Error(), tt.errSubstr)
				}
				require.False(t, drop)
				require.Nil(t, out)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantDrop, drop)
			if tt.wantDrop {
				require.Nil(t, out)
				return
			}
			require.JSONEq(t, tt.wantOut, string(out))
		})
	}
}

func TestJQCompileUndefinedFunction(t *testing.T) {
	t.Parallel()

	_, err := Compile(LangJQ, "no_such_func(1)")
	require.Error(t, err)
	require.Contains(t, err.Error(), "jq compile")
	var re *RuntimeError
	require.False(t, errors.As(err, &re), "compile errors must not be RuntimeError")
}

func TestJQCompileParseError(t *testing.T) {
	t.Parallel()

	_, err := Compile(LangJQ, "{{")
	require.Error(t, err)
	require.Contains(t, err.Error(), "jq parse")
	var re *RuntimeError
	require.False(t, errors.As(err, &re), "parse errors must not be RuntimeError")
}

func TestJQLanguage(t *testing.T) {
	t.Parallel()

	eng, err := Compile(LangJQ, ".")
	require.NoError(t, err)
	require.Equal(t, "jq", eng.Language())
}

func TestJQInvalidJSON(t *testing.T) {
	t.Parallel()

	eng, err := Compile(LangJQ, ".")
	require.NoError(t, err)

	out, drop, err := eng.Apply([]byte(`{`), nil)
	require.Error(t, err)
	var re *RuntimeError
	require.ErrorAs(t, err, &re)
	require.Equal(t, LangJQ, re.Language)
	require.False(t, drop)
	require.Nil(t, out)
}

func BenchmarkApplyJQ(b *testing.B) {
	eng, err := Compile(LangJQ, "{id, status}")
	require.NoError(b, err)
	raw := []byte(sampleJQRaw)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := eng.Apply(raw, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}
