package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1GB", 1 << 30, false},
		{"1gb", 1 << 30, false},
		{"500MB", 500 << 20, false},
		{"512KB", 512 << 10, false},
		{"2TB", 2 << 40, false},
		{"1024", 1024, false},
		{"1024B", 1024, false},
		{"1.5GB", 1610612736, false},
		{" 256MB ", 256 << 20, false},
		{"abc", 0, true},
		{"-1GB", 0, true},
		{"GB", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSize(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
