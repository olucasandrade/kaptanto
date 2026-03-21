package statsd

import (
	"testing"
)

// TestParseCPUPct tests the parseCPUPct helper.
func TestParseCPUPct(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    float64
		wantErr bool
	}{
		{name: "normal percentage", input: "0.13%", want: 0.13, wantErr: false},
		{name: "full load percentage", input: "100.00%", want: 100.0, wantErr: false},
		{name: "empty string", input: "", want: 0, wantErr: true},
		{name: "no percent sign", input: "42.5", want: 42.5, wantErr: false},
		{name: "zero", input: "0.00%", want: 0.0, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCPUPct(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseCPUPct(%q) expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Errorf("parseCPUPct(%q) unexpected error: %v", tc.input, err)
				return
			}
			if got != tc.want {
				t.Errorf("parseCPUPct(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestParseVmRSS tests the parseVmRSS helper.
func TestParseVmRSS(t *testing.T) {
	sampleStatus := `Name:   postgres
State:  S (sleeping)
Pid:    1234
VmPeak:   123456 kB
VmSize:   110000 kB
VmRSS:     45678 kB
VmData:    30000 kB
Threads: 8
`

	t.Run("parses VmRSS correctly", func(t *testing.T) {
		got, err := parseVmRSS(sampleStatus)
		if err != nil {
			t.Fatalf("parseVmRSS unexpected error: %v", err)
		}
		if got != 45678 {
			t.Errorf("parseVmRSS = %d, want 45678", got)
		}
	})

	t.Run("returns error when VmRSS missing", func(t *testing.T) {
		noRSSStatus := `Name:   postgres
State:  S (sleeping)
VmSize:   110000 kB
`
		_, err := parseVmRSS(noRSSStatus)
		if err == nil {
			t.Error("parseVmRSS expected error for missing VmRSS line, got nil")
		}
	})

	t.Run("parses single digit VmRSS", func(t *testing.T) {
		tiny := "VmRSS:\t     512 kB\n"
		got, err := parseVmRSS(tiny)
		if err != nil {
			t.Fatalf("parseVmRSS unexpected error: %v", err)
		}
		if got != 512 {
			t.Errorf("parseVmRSS = %d, want 512", got)
		}
	})
}
