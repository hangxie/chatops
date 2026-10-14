package kube

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// A literal env value this long keeps its last redactKeep characters; shorter values are fully redacted.
const (
	redactMinLength = 12
	redactKeep      = 3
)

// cleanMeta drops server bookkeeping that costs tokens and tells a reader nothing.
func CleanMeta(m *metav1.ObjectMeta) {
	m.ManagedFields = nil
	m.ResourceVersion = ""
	m.UID = ""
	m.GenerateName = ""
	delete(m.Annotations, lastAppliedAnnotation)
	if len(m.Annotations) == 0 {
		m.Annotations = nil
	}
}

// redactPodSpec masks literal env values in every container, since describe output is relayed into chat.
func RedactPodSpec(spec *corev1.PodSpec) {
	for i := range spec.InitContainers {
		redactEnv(spec.InitContainers[i].Env)
	}
	for i := range spec.Containers {
		redactEnv(spec.Containers[i].Env)
	}
}

// redactEnv leaves valueFrom references alone: they name a Secret or ConfigMap key, not its value.
func redactEnv(env []corev1.EnvVar) {
	for i := range env {
		if env[i].Value != "" {
			env[i].Value = redact(env[i].Value)
		}
	}
}

func redact(value string) string {
	r := []rune(value)
	if len(r) < redactMinLength {
		return "<redacted>"
	}
	return "<redacted …" + string(r[len(r)-redactKeep:]) + ">"
}

func ToYAML(obj any) (string, error) {
	b, err := yaml.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
