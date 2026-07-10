package transform

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/stretchr/testify/require"
)

func sampleEvent() *event.ChangeEvent {
	return &event.ChangeEvent{
		ID:             ulid.MustParse("01HXYZ00000000000000000000"),
		IdempotencyKey: "pg:public.orders:1:insert:0/1",
		Timestamp:      time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		Source:         "postgres",
		Operation:      event.OpInsert,
		Database:       "app",
		Schema:         "public",
		Table:          "orders",
		Key:            json.RawMessage(`{"id":1}`),
		Before:         nil,
		After:          json.RawMessage(`{"id":1,"status":"open"}`),
		Metadata:       map[string]any{"lsn": "0/1"},
	}
}

func TestGoTemplateGolden(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
		wantOut    string
		wantDrop   bool
		wantErr    bool
	}{
		{
			name:       "rendered table",
			expression: `{{.Schema}}.{{.Table}}`,
			wantOut:    "public.orders",
		},
		{
			name:       "rendered operation",
			expression: `op={{.Operation}}`,
			wantOut:    "op=insert",
		},
		{
			name:       "empty output drops",
			expression: `{{if false}}x{{end}}`,
			wantDrop:   true,
		},
		{
			name:       "whitespace-only drops",
			expression: "  \n\t  ",
			wantDrop:   true,
		},
		{
			name:       "execution error",
			expression: `{{.MissingField.Nested}}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eng, err := Compile(LangGoTemplate, tt.expression)
			require.NoError(t, err)

			out, drop, err := eng.Apply(nil, sampleEvent())
			if tt.wantErr {
				require.Error(t, err)
				var re *RuntimeError
				require.ErrorAs(t, err, &re)
				require.Equal(t, LangGoTemplate, re.Language)
				require.NotNil(t, re.Cause)
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
			require.Equal(t, tt.wantOut, string(out))
		})
	}
}

func TestGoTemplateParseError(t *testing.T) {
	t.Parallel()

	_, err := Compile(LangGoTemplate, "{{")
	require.Error(t, err)
	require.Contains(t, err.Error(), "go-template parse")
	var re *RuntimeError
	require.False(t, errors.As(err, &re), "parse errors must not be RuntimeError")
}

func TestGoTemplateLanguage(t *testing.T) {
	t.Parallel()

	eng, err := Compile(LangGoTemplate, "{{.Table}}")
	require.NoError(t, err)
	require.Equal(t, "go-template", eng.Language())
}

func BenchmarkApply(b *testing.B) {
	eng, err := Compile(LangGoTemplate, `{"table":"{{.Table}}","op":"{{.Operation}}"}`)
	require.NoError(b, err)
	ev := sampleEvent()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := eng.Apply(nil, ev)
		if err != nil {
			b.Fatal(err)
		}
	}
}
