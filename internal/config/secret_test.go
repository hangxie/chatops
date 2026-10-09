package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Secret(t *testing.T) {
	t.Setenv("CHATOPS_TEST_SECRET", "s3cret")
	t.Setenv("CHATOPS_TEST_EMPTY", "")

	tests := map[string]struct {
		env    string
		want   string
		errMsg string
	}{
		"unset-name": {env: "", want: ""},
		"present":    {env: "CHATOPS_TEST_SECRET", want: "s3cret"},
		"empty":      {env: "CHATOPS_TEST_EMPTY", errMsg: "environment variable CHATOPS_TEST_EMPTY is empty"},
		"missing":    {env: "CHATOPS_TEST_MISSING", errMsg: "environment variable CHATOPS_TEST_MISSING is not set"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Secret(tc.env)
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
