package tool_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

// sampleDescriptor is a small, valid descriptor reused across the tests.
func sampleDescriptor() tool.Descriptor {
	return tool.Descriptor{
		Description: "check external service status",
		Parameters: []tool.Param{
			{Name: "service", Type: "string", Required: true, Description: "the service name"},
		},
	}
}

func Test_NewRegistry_rejects_malformed_descriptor(t *testing.T) {
	opener := fakeOpener(&fakeTool{}, nil)
	testCases := map[string]*tool.Descriptor{
		"nil-descriptor":   nil,
		"no-description":   {Parameters: []tool.Param{{Name: "p"}}},
		"param-no-name":    {Description: "x", Parameters: []tool.Param{{Name: ""}}},
		"duplicate-param":  {Description: "x", Parameters: []tool.Param{{Name: "p"}, {Name: "p"}}},
		"unsupported-type": {Description: "x", Parameters: []tool.Param{{Name: "p", Type: "object"}}},
	}

	for name, desc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, func() {
				tool.NewRegistry(tool.Backend{Scheme: "status", Opener: opener, Descriptor: desc})
			})
		})
	}
}

func Test_Descriptor_Validate(t *testing.T) {
	testCases := map[string]struct {
		desc    tool.Descriptor
		wantErr string
	}{
		"valid":            {desc: sampleDescriptor()},
		"no-description":   {desc: tool.Descriptor{Parameters: []tool.Param{{Name: "p"}}}, wantErr: "no description"},
		"param-no-name":    {desc: tool.Descriptor{Description: "x", Parameters: []tool.Param{{Name: ""}}}, wantErr: "parameter with no name"},
		"duplicate-param":  {desc: tool.Descriptor{Description: "x", Parameters: []tool.Param{{Name: "p"}, {Name: "p"}}}, wantErr: `duplicate parameter "p"`},
		"unsupported-type": {desc: tool.Descriptor{Description: "x", Parameters: []tool.Param{{Name: "p", Type: "object"}}}, wantErr: `unsupported type "object"`},
		"all-scalar-types": {desc: tool.Descriptor{Description: "x", Parameters: []tool.Param{
			{Name: "s", Type: "string"},
			{Name: "n", Type: "number"},
			{Name: "i", Type: "integer"},
			{Name: "b", Type: "boolean"},
			{Name: "d"}, // empty type defaults to string, allowed
		}}},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := tc.desc.Validate()
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func Test_Descriptor_Clone(t *testing.T) {
	orig := tool.Descriptor{Description: "scale a deployment", Parameters: []tool.Param{{Name: "replicas", Type: "integer"}}}
	clone := orig.Clone()
	require.Equal(t, orig, clone)

	// Mutating the clone's Parameters must not affect the original: they share
	// no backing storage.
	clone.Parameters[0].Name = "changed"
	require.Equal(t, "replicas", orig.Parameters[0].Name)

	// A descriptor with no parameters clones to an equal value.
	require.Equal(t, tool.Descriptor{Description: "x"}, tool.Descriptor{Description: "x"}.Clone())
}

func Test_Registry_Descriptor(t *testing.T) {
	desc := sampleDescriptor()
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "status", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: &desc},
	)

	got, ok := reg.Descriptor("status")
	require.True(t, ok)
	require.Equal(t, desc, got)

	// Lookup is case-insensitive, matching url.Parse scheme lowercasing.
	got, ok = reg.Descriptor("STATUS")
	require.True(t, ok)
	require.Equal(t, desc, got)

	// Only an unknown scheme reports absence.
	_, ok = reg.Descriptor("bogus")
	require.False(t, ok)
}

func Test_Registry_Descriptor_is_deep_copied(t *testing.T) {
	// A descriptor with parameters exercises deep copying of the Parameters
	// slice.
	desc := tool.Descriptor{
		Description: "scale a deployment",
		Parameters:  []tool.Param{{Name: "replicas", Type: "integer", Required: true}},
	}
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "k8s", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: &desc},
	)

	// Mutating the caller's descriptor after construction must not leak in.
	desc.Parameters[0].Name = "mutated"

	got, ok := reg.Descriptor("k8s")
	require.True(t, ok)
	require.Equal(t, "replicas", got.Parameters[0].Name)

	// Mutating a returned descriptor must not affect later reads.
	got.Parameters[0].Name = "changed"
	again, ok := reg.Descriptor("k8s")
	require.True(t, ok)
	require.Equal(t, "replicas", again.Parameters[0].Name)
}

func Test_Registry_Select_deep_copies_descriptors(t *testing.T) {
	desc := tool.Descriptor{
		Description: "scale a deployment",
		Parameters:  []tool.Param{{Name: "replicas", Type: "integer"}},
	}
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "k8s", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: &desc},
	)

	selected, err := reg.Select("k8s")
	require.NoError(t, err)

	// Mutating a descriptor from the derived registry must not affect the
	// parent registry.
	got, ok := selected.Descriptor("k8s")
	require.True(t, ok)
	got.Parameters[0].Name = "changed"

	parent, ok := reg.Descriptor("k8s")
	require.True(t, ok)
	require.Equal(t, "replicas", parent.Parameters[0].Name)
}

func Test_Registry_Select_carries_descriptors(t *testing.T) {
	statusDesc := sampleDescriptor()
	pingDesc := tool.Descriptor{Description: "ping"}
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "status", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: &statusDesc},
		tool.Backend{Scheme: "ping", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: &pingDesc},
	)

	selected, err := reg.Select("status")
	require.NoError(t, err)

	got, ok := selected.Descriptor("status")
	require.True(t, ok)
	require.Equal(t, statusDesc, got)

	// A tool not selected is absent from the derived registry.
	_, ok = selected.Descriptor("ping")
	require.False(t, ok)
}
