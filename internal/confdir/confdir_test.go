package confdir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type item struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

type nested struct {
	Timeout string `yaml:"timeout"`
	Retries int    `yaml:"retries"`
}

type sample struct {
	Items  []item   `yaml:"items"`
	Limit  int      `yaml:"limit"`
	Label  string   `yaml:"label"`
	Nested nested   `yaml:"nested"`
	Tags   []string `yaml:"tags"`
}

// setup writes files into a new directory and returns it.
func setup(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	return dir
}

func Test_Decode(t *testing.T) {
	tests := map[string]struct {
		files map[string]string
		want  sample
	}{
		"empty-dir": {
			want: sample{},
		},
		"one-file": {
			files: map[string]string{"00-default.yaml": "limit: 5\nitems:\n  - name: a\n"},
			want:  sample{Limit: 5, Items: []item{{Name: "a"}}},
		},
		"lists-append-in-file-name-order": {
			files: map[string]string{"20-b.yaml": "items:\n  - name: b\n", "00-default.yaml": "items:\n  - name: default\n", "10-a.yaml": "items:\n  - name: a\n"},
			want:  sample{Items: []item{{Name: "default"}, {Name: "a"}, {Name: "b"}}},
		},
		"scalar-later-file-wins": {
			files: map[string]string{"00-default.yaml": "limit: 5\nlabel: shipped\n", "10-a.yaml": "limit: 10\n", "20-b.yaml": "limit: 20\n"},
			want:  sample{Limit: 20, Label: "shipped"},
		},
		"mappings-merge-key-by-key": {
			files: map[string]string{"00-default.yaml": "nested:\n  timeout: 5s\n  retries: 1\n", "site.yaml": "nested:\n  retries: 3\n"},
			want:  sample{Nested: nested{Timeout: "5s", Retries: 3}},
		},
		"new-keys-are-added": {
			files: map[string]string{"00-default.yaml": "limit: 5\n", "site.yaml": "label: site\ntags: [x]\n"},
			want:  sample{Limit: 5, Label: "site", Tags: []string{"x"}},
		},
		"list-items-are-not-merged": {
			files: map[string]string{"00-default.yaml": "items:\n  - name: a\n    url: https://a.example\n", "site.yaml": "items:\n  - name: a\n"},
			want:  sample{Items: []item{{Name: "a", URL: "https://a.example"}, {Name: "a"}}},
		},
		"null-keeps-earlier-value": {
			files: map[string]string{"00-default.yaml": "limit: 5\nitems:\n  - name: a\n", "site.yaml": "limit:\nitems: ~\n"},
			want:  sample{Limit: 5, Items: []item{{Name: "a"}}},
		},
		"earlier-null-is-filled": {
			files: map[string]string{"00-default.yaml": "items:\n", "site.yaml": "items:\n  - name: a\n"},
			want:  sample{Items: []item{{Name: "a"}}},
		},
		"empty-files": {
			files: map[string]string{"00-default.yaml": "", "comment.yaml": "# nothing yet\n", "site.yaml": "limit: 7\n"},
			want:  sample{Limit: 7},
		},
		"anchors-within-a-file": {
			files: map[string]string{"00-default.yaml": "label: &l shipped\ntags: [*l]\n", "site.yaml": "nested: &n\n  retries: 2\n"},
			want:  sample{Label: "shipped", Tags: []string{"shipped"}, Nested: nested{Retries: 2}},
		},
		"only-visible-yaml-files": {
			files: map[string]string{
				"00-default.yaml": "limit: 1\n", "00-default.yaml.dpkg-dist": "limit: 9\n", "00-default.yaml.rpmnew": "limit: 9\n", "x.yaml~": "limit: 9\n",
				"x.yml": "limit: 9\n", ".#x.yaml": "limit: 9\n", ".hidden.yaml": "limit: 9\n", "README": "not yaml", "kubeconfig": "apiVersion: v1\n",
			},
			want: sample{Limit: 1},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := setup(t, tc.files)
			require.NoError(t, os.Mkdir(filepath.Join(dir, "sub.d"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "sub.d", "x.yaml"), []byte("limit: 9\n"), 0o600))
			var got sample
			require.NoError(t, Decode("sample", dir, &got))
			require.Equal(t, tc.want, got)
		})
	}
}

func Test_Decode_errors(t *testing.T) {
	tests := map[string]struct {
		files  map[string]string
		errMsg string
	}{
		"unknown-field":         {files: map[string]string{"00-default.yaml": "limit: 1\n", "site.yaml": "include: [x.yaml]\n"}, errMsg: "site.yaml: yaml: unmarshal errors:\n  line 1: field include not found"},
		"wrong-type":            {files: map[string]string{"site.yaml": "limit: many\n"}, errMsg: "site.yaml: yaml: unmarshal errors"},
		"unparsable":            {files: map[string]string{"site.yaml": "items: [\n"}, errMsg: "parse sample config"},
		"list-over-scalar":      {files: map[string]string{"00-default.yaml": "label: x\n", "site.yaml": "label: [x]\n"}, errMsg: "parse sample config"},
		"kind-mismatch":         {files: map[string]string{"00-default.yaml": "items:\n  - name: a\n", "10-a.yaml": "label: x\n", "20-b.yaml": "items:\n  name: b\n"}, errMsg: "20-b.yaml"},
		"alias-to-another-file": {files: map[string]string{"00-default.yaml": "label: &l x\n", "site.yaml": "tags: [*l]\n"}, errMsg: "site.yaml: yaml: unknown anchor 'l' referenced"},
		"top-level-not-mapping": {files: map[string]string{"site.yaml": "- a\n"}, errMsg: "site.yaml: top level must be a mapping"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got sample
			require.ErrorContains(t, Decode("sample", setup(t, tc.files), &got), tc.errMsg)
		})
	}
}

func Test_merge(t *testing.T) {
	parse := func(text string) *yaml.Node {
		var doc yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(text), &doc))
		return doc.Content[0]
	}
	tests := map[string]struct {
		dst, src string
		want     string
		errMsg   string
	}{
		"scalar-wins":         {dst: "a: 1\n", src: "a: 2\n", want: "a: 2\n"},
		"list-appends":        {dst: "a: [1]\n", src: "a: [2, 3]\n", want: "a: [1, 2, 3]\n"},
		"deep-mapping":        {dst: "a: {b: 1, c: 2}\n", src: "a: {c: 3, d: 4}\n", want: "a: {b: 1, c: 3, d: 4}\n"},
		"null-keeps":          {dst: "a: 1\n", src: "a: null\n", want: "a: 1\n"},
		"null-is-replaced":    {dst: "a:\n", src: "a: [1]\n", want: "a: [1]\n"},
		"alias-is-resolved":   {dst: "a: &x {b: 1}\nc: *x\n", src: "c: {d: 2}\n", want: "a: &x {b: 1}\nc: {b: 1, d: 2}\n"},
		"list-into-scalar":    {dst: "a: {b: 1}\n", src: "a: {b: [1]}\n", errMsg: "a.b: cannot merge a list into a scalar"},
		"mapping-into-list":   {dst: "a: [1]\n", src: "a: {b: 1}\n", errMsg: "a: cannot merge a mapping into a list"},
		"scalar-into-mapping": {dst: "a: {b: 1}\n", src: "a: 1\n", errMsg: "a: cannot merge a scalar into a mapping"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dst := parse(tc.dst)
			err := merge(dst, parse(tc.src), "")
			if tc.errMsg != "" {
				require.EqualError(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err)
			var got, want any
			require.NoError(t, dst.Decode(&got))
			require.NoError(t, yaml.Unmarshal([]byte(tc.want), &want))
			require.Equal(t, want, got)
		})
	}
}

func Test_Decode_directory_errors(t *testing.T) {
	t.Run("missing-directory", func(t *testing.T) {
		var got sample
		require.ErrorContains(t, Decode("sample", filepath.Join(t.TempDir(), "none.d"), &got), "read sample config directory")
	})
	t.Run("path-is-a-file", func(t *testing.T) {
		path := filepath.Join(setup(t, map[string]string{"a.yaml": "limit: 1\n"}), "a.yaml")
		var got sample
		require.ErrorContains(t, Decode("sample", path, &got), "read sample config directory")
	})
	t.Run("yaml-named-directory", func(t *testing.T) {
		dir := setup(t, nil)
		require.NoError(t, os.Mkdir(filepath.Join(dir, "c.yaml"), 0o700))
		var got sample
		require.ErrorContains(t, Decode("sample", dir, &got), "read sample config")
	})
}

func Test_listFiles(t *testing.T) {
	dir := setup(t, map[string]string{"b.yaml": "", "a.yaml": "", "c.txt": "", ".d.yaml": ""})
	got, err := listFiles(dir)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")}, got)
}
