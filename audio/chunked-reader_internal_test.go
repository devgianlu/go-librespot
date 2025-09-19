//go:build test_unit

package audio

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseContentRange(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		wantStart   int64
		wantEnd     int64
		wantSize    int64
		wantErr     bool
		errContains string
	}{
		{
			name:      "valid range",
			header:    "bytes 0-511/1024",
			wantStart: 0,
			wantEnd:   511,
			wantSize:  1024,
			wantErr:   false,
		},
		{
			name:      "middle range",
			header:    "bytes 512-1023/2048",
			wantStart: 512,
			wantEnd:   1023,
			wantSize:  2048,
			wantErr:   false,
		},
		{
			name:        "no header",
			header:      "",
			wantErr:     true,
			errContains: "no Content-Range header",
		},
		{
			name:        "invalid format",
			header:      "invalid format",
			wantErr:     true,
			errContains: "invalid content range header",
		},
		{
			name:        "invalid start",
			header:      "bytes abc-511/1024",
			wantErr:     true,
			errContains: "invalid content range header",
		},
		{
			name:        "invalid end",
			header:      "bytes 0-abc/1024",
			wantErr:     true,
			errContains: "invalid content range header",
		},
		{
			name:        "invalid size",
			header:      "bytes 0-511/abc",
			wantErr:     true,
			errContains: "invalid content range header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				Header: http.Header{},
			}
			if tt.header != "" {
				resp.Header.Set("Content-Range", tt.header)
			}

			start, end, size, err := parseContentRange(resp)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantStart, start)
				assert.Equal(t, tt.wantEnd, end)
				assert.Equal(t, tt.wantSize, size)
			}
		})
	}
}
