package tool_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hangxie/chatops/tool"
)

// sampleDescriptor is a small, valid descriptor reused across the tests.
func sampleDescriptor() tool.Descriptor {
	return tool.Descriptor{
		Summary: "check external service status",
		Actions: []tool.Action{
			{
				Name:        "check",
				Description: "check one service",
				TakesTarget: true,
				TargetDesc:  "the service name",
			},
			{Name: "list", Description: "list services"},
		},
	}
}

func Test_NewRegistry_rejects_malformed_descriptor(t *testing.T) {
	opener := fakeOpener(&fakeTool{}, nil)
	testCases := map[string]*tool.Descriptor{
		"nil-descriptor":   nil,
		"no-actions":       {Summary: "x"},
		"action-no-name":   {Summary: "x", Actions: []tool.Action{{Name: ""}}},
		"one-good-one-bad": {Summary: "x", Actions: []tool.Action{{Name: "ok"}, {Name: ""}}},
		"duplicate-action": {Summary: "x", Actions: []tool.Action{{Name: "dup"}, {Name: "dup"}}},
		"param-no-name":    {Summary: "x", Actions: []tool.Action{{Name: "a", Parameters: []tool.Param{{Name: ""}}}}},
		"duplicate-param":  {Summary: "x", Actions: []tool.Action{{Name: "a", Parameters: []tool.Param{{Name: "p"}, {Name: "p"}}}}},
		"unsupported-type": {Summary: "x", Actions: []tool.Action{{Name: "a", Parameters: []tool.Param{{Name: "p", Type: "object"}}}}},
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
		"no-actions":       {desc: tool.Descriptor{Summary: "x"}, wantErr: "no actions"},
		"action-no-name":   {desc: tool.Descriptor{Actions: []tool.Action{{Name: ""}}}, wantErr: "action with no name"},
		"duplicate-action": {desc: tool.Descriptor{Actions: []tool.Action{{Name: "a"}, {Name: "a"}}}, wantErr: `duplicate action "a"`},
		"param-no-name":    {desc: tool.Descriptor{Actions: []tool.Action{{Name: "a", Parameters: []tool.Param{{Name: ""}}}}}, wantErr: "parameter with no name"},
		"duplicate-param":  {desc: tool.Descriptor{Actions: []tool.Action{{Name: "a", Parameters: []tool.Param{{Name: "p"}, {Name: "p"}}}}}, wantErr: `duplicate parameter "p"`},
		"unsupported-type": {desc: tool.Descriptor{Actions: []tool.Action{{Name: "a", Parameters: []tool.Param{{Name: "p", Type: "object"}}}}}, wantErr: `unsupported type "object"`},
		"all-scalar-types": {desc: tool.Descriptor{Actions: []tool.Action{{Name: "a", Parameters: []tool.Param{
			{Name: "s", Type: "string"},
			{Name: "n", Type: "number"},
			{Name: "i", Type: "integer"},
			{Name: "b", Type: "boolean"},
			{Name: "d"}, // empty type defaults to string, allowed
		}}}}},
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
	orig := tool.Descriptor{Summary: "s", Actions: []tool.Action{{
		Name:       "scale",
		Parameters: []tool.Param{{Name: "replicas", Type: "integer"}},
	}}}
	clone := orig.Clone()
	require.Equal(t, orig, clone)

	// Mutating the clone's nested Actions and Parameters must not affect the
	// original: they share no backing storage.
	clone.Actions[0].Name = "changed"
	clone.Actions[0].Parameters[0].Name = "changed"
	require.Equal(t, "scale", orig.Actions[0].Name)
	require.Equal(t, "replicas", orig.Actions[0].Parameters[0].Name)

	// A descriptor with no actions clones to an equal value.
	require.Equal(t, tool.Descriptor{Summary: "x"}, tool.Descriptor{Summary: "x"}.Clone())
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
	// A descriptor with parameters exercises deep copying of both the
	// Actions and the nested Parameters slices.
	desc := tool.Descriptor{
		Summary: "deploy",
		Actions: []tool.Action{{
			Name:        "scale",
			TakesTarget: true,
			Parameters:  []tool.Param{{Name: "replicas", Type: "integer", Required: true}},
		}},
	}
	reg := tool.NewRegistry(
		tool.Backend{Scheme: "k8s", Opener: fakeOpener(&fakeTool{}, nil), Descriptor: &desc},
	)

	// Mutating the caller's descriptor after construction must not leak in.
	desc.Actions[0].Name = "mutated"
	desc.Actions[0].Parameters[0].Name = "mutated"

	got, ok := reg.Descriptor("k8s")
	require.True(t, ok)
	require.Equal(t, "scale", got.Actions[0].Name)
	require.Equal(t, "replicas", got.Actions[0].Parameters[0].Name)

	// Mutating a returned descriptor must not affect later reads.
	got.Actions[0].Name = "changed"
	got.Actions[0].Parameters[0].Name = "changed"
	again, ok := reg.Descriptor("k8s")
	require.True(t, ok)
	require.Equal(t, "scale", again.Actions[0].Name)
	require.Equal(t, "replicas", again.Actions[0].Parameters[0].Name)
}

func Test_Registry_Select_deep_copies_descriptors(t *testing.T) {
	desc := tool.Descriptor{
		Summary: "deploy",
		Actions: []tool.Action{{
			Name:       "scale",
			Parameters: []tool.Param{{Name: "replicas", Type: "integer"}},
		}},
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
	got.Actions[0].Parameters[0].Name = "changed"

	parent, ok := reg.Descriptor("k8s")
	require.True(t, ok)
	require.Equal(t, "replicas", parent.Actions[0].Parameters[0].Name)
}

func Test_Registry_Select_carries_descriptors(t *testing.T) {
	statusDesc := sampleDescriptor()
	pingDesc := tool.Descriptor{Summary: "ping", Actions: []tool.Action{{Name: "ping"}}}
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
