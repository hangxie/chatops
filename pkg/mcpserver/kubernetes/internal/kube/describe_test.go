package kube

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_redact(t *testing.T) {
	tests := map[string]struct {
		value, want string
	}{
		"short":          {value: "hunter2", want: "<redacted>"},
		"one below":      {value: "abcdefghijk", want: "<redacted>"},
		"at the minimum": {value: "abcdefghijkl", want: "<redacted …jkl>"},
		"long":           {value: "sk-0123456789abcdefx7Q", want: "<redacted …x7Q>"},
		"multibyte":      {value: "密码密码密码密码密码密码", want: "<redacted …码密码>"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, redact(tc.value))
		})
	}
}

func Test_redactPodSpec(t *testing.T) {
	ref := &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password",
	}}
	spec := corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "init", Env: []corev1.EnvVar{{Name: "TOKEN", Value: "init-token-abc123"}}}},
		Containers: []corev1.Container{{Name: "main", Env: []corev1.EnvVar{
			{Name: "LOG_LEVEL", Value: "debug"},
			{Name: "DB_PASSWORD", ValueFrom: ref},
			{Name: "EMPTY"},
		}}},
	}

	RedactPodSpec(&spec)
	require.Equal(t, "<redacted …123>", spec.InitContainers[0].Env[0].Value)
	require.Equal(t, "<redacted>", spec.Containers[0].Env[0].Value)
	require.Equal(t, ref, spec.Containers[0].Env[1].ValueFrom)
	require.Empty(t, spec.Containers[0].Env[1].Value)
	require.Empty(t, spec.Containers[0].Env[2].Value)
}

func Test_cleanMeta(t *testing.T) {
	m := metav1.ObjectMeta{
		Name: "web", ResourceVersion: "42", UID: "uid", GenerateName: "web-",
		ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		Annotations:   map[string]string{lastAppliedAnnotation: "{}", "keep": "me"},
	}
	CleanMeta(&m)
	require.Equal(t, metav1.ObjectMeta{Name: "web", Annotations: map[string]string{"keep": "me"}}, m)

	only := metav1.ObjectMeta{Annotations: map[string]string{lastAppliedAnnotation: "{}"}}
	CleanMeta(&only)
	require.Nil(t, only.Annotations)
}

func Test_toYAML(t *testing.T) {
	text, err := ToYAML(map[string]any{"kind": "Deployment"})
	require.NoError(t, err)
	require.Equal(t, "kind: Deployment\n", text)

	_, err = ToYAML(map[string]any{"bad": func() {}})
	require.Error(t, err)
}
