package kube

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Clients_ResultLimit(t *testing.T) {
	tests := map[string]struct {
		max  int
		want int
	}{
		"unset uses default":    {max: 0, want: DefaultMaxResults},
		"negative uses default": {max: -1, want: DefaultMaxResults},
		"set":                   {max: 7, want: 7},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, Clients{MaxResults: tc.max}.ResultLimit())
		})
	}
}

func Test_Cap(t *testing.T) {
	tests := map[string]struct {
		items     []int
		limit     int
		want      []int
		wantTotal int
	}{
		"under": {items: []int{1, 2}, limit: 3, want: []int{1, 2}, wantTotal: 2},
		"exact": {items: []int{1, 2}, limit: 2, want: []int{1, 2}, wantTotal: 2},
		"over":  {items: []int{1, 2, 3}, limit: 2, want: []int{1, 2}, wantTotal: 3},
		"nil":   {items: nil, limit: 2, want: nil, wantTotal: 0},
		"zero":  {items: []int{1}, limit: 0, want: []int{}, wantTotal: 1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, total := Cap(tc.items, tc.limit)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.wantTotal, total)
		})
	}
}

func Test_CapNote(t *testing.T) {
	require.Empty(t, CapNote(2, 2, "pods"))
	require.Empty(t, CapNote(0, 0, "pods"))
	require.Equal(t, "\n(showing the first 2 of 5 pods)", CapNote(2, 5, "pods"))
}
