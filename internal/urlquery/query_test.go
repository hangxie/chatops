package urlquery

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Validate(t *testing.T) {
	testCases := map[string]struct {
		query  url.Values
		errMsg string
	}{
		"valid":    {query: url.Values{"model": {"m"}, "flag": {"true"}}},
		"unknown":  {query: url.Values{"other": {"value"}}, errMsg: `unknown query parameter "other"`},
		"repeated": {query: url.Values{"model": {"a", "b"}}, errMsg: `query parameter "model" must appear once`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := Validate(tc.query, "model", "flag")
			if tc.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.errMsg)
		})
	}
}

func Test_Bool(t *testing.T) {
	testCases := map[string]struct {
		query    url.Values
		expected bool
		errMsg   string
	}{
		"absent":        {},
		"true":          {query: url.Values{"flag": {"true"}}, expected: true},
		"false":         {query: url.Values{"flag": {"false"}}},
		"invalid":       {query: url.Values{"flag": {"yes"}}, errMsg: "flag must be true or false"},
		"missing-value": {query: url.Values{"flag": nil}, errMsg: `query parameter "flag" must appear once`},
		"repeated":      {query: url.Values{"flag": {"true", "false"}}, errMsg: `query parameter "flag" must appear once`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			actual, err := Bool(tc.query, "flag")
			if tc.errMsg == "" {
				require.NoError(t, err)
				require.Equal(t, tc.expected, actual)
				return
			}
			require.EqualError(t, err, tc.errMsg)
		})
	}
}
