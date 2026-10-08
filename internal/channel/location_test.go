package channel

import (
	"math"
	"testing"
)

func TestValidateCoords(t *testing.T) {
	tests := []struct {
		name    string
		lat     float64
		lon     float64
		wantErr bool
	}{
		{"valid berlin", 52.51627, 13.37775, false},
		{"lat upper bound", 90, 0, false},
		{"lat lower bound", -90, 0, false},
		{"lon upper bound", 0, 180, false},
		{"lon lower bound", 0, -180, false},
		{"lat above range", 90.0001, 0, true},
		{"lat below range", -91, 0, true},
		{"lon above range", 0, 180.5, true},
		{"lon below range", 0, -181, true},
		{"lat NaN", math.NaN(), 0, true},
		{"lon NaN", 0, math.NaN(), true},
		{"lat +Inf", math.Inf(1), 0, true},
		{"lon -Inf", 0, math.Inf(-1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCoords(tt.lat, tt.lon)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCoords(%v, %v) error = %v, wantErr %v", tt.lat, tt.lon, err, tt.wantErr)
			}
		})
	}
}

func TestFormatCoords(t *testing.T) {
	if got := FormatCoords(52.51627, 13.37775); got != "52.516270, 13.377750" {
		t.Fatalf("FormatCoords = %q", got)
	}
	if got := FormatCoords(-33.865143, -70.995777); got != "-33.865143, -70.995777" {
		t.Fatalf("FormatCoords negative = %q", got)
	}
}

func TestClampAccuracy(t *testing.T) {
	tests := []struct {
		in, want float64
	}{{-1, 0}, {0, 0}, {35, 35}, {1500, 1500}, {2000, 1500}, {math.NaN(), 0}}
	for _, tt := range tests {
		if got := ClampAccuracy(tt.in); got != tt.want {
			t.Fatalf("ClampAccuracy(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
