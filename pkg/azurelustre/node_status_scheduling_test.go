/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azurelustre

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNodeStatusSchedulingSelectors(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker-1", Labels: map[string]string{"os": "linux", "generation": "3"},
	}}
	for _, test := range []struct {
		name     string
		key      string
		operator corev1.NodeSelectorOperator
		values   []string
		field    bool
		want     bool
	}{
		{"label matches", "os", corev1.NodeSelectorOpIn, []string{"linux"}, false, true},
		{"label differs", "os", corev1.NodeSelectorOpIn, []string{"windows"}, false, false},
		{"excluded label", "os", corev1.NodeSelectorOpNotIn, []string{"linux"}, false, false},
		{"allowed label", "os", corev1.NodeSelectorOpNotIn, []string{"windows"}, false, true},
		{"missing excluded label", "absent", corev1.NodeSelectorOpNotIn, []string{"linux"}, false, true},
		{"label exists", "os", corev1.NodeSelectorOpExists, nil, false, true},
		{"label missing", "absent", corev1.NodeSelectorOpExists, nil, false, false},
		{"absence required", "absent", corev1.NodeSelectorOpDoesNotExist, nil, false, true},
		{"unexpected label", "os", corev1.NodeSelectorOpDoesNotExist, nil, false, false},
		{"greater than", "generation", corev1.NodeSelectorOpGt, []string{"2"}, false, true},
		{"not greater than", "generation", corev1.NodeSelectorOpGt, []string{"3"}, false, false},
		{"less than", "generation", corev1.NodeSelectorOpLt, []string{"4"}, false, true},
		{"not less than", "generation", corev1.NodeSelectorOpLt, []string{"3"}, false, false},
		{"numeric label missing", "absent", corev1.NodeSelectorOpGt, []string{"2"}, false, false},
		{"multiple numeric operands", "generation", corev1.NodeSelectorOpGt, []string{"1", "2"}, false, false},
		{"nonnumeric label", "os", corev1.NodeSelectorOpGt, []string{"2"}, false, false},
		{"nonnumeric operand", "generation", corev1.NodeSelectorOpLt, []string{"invalid"}, false, false},
		{"unknown operator", "os", corev1.NodeSelectorOperator("Invalid"), nil, false, false},
		{"node name matches", "metadata.name", corev1.NodeSelectorOpIn, []string{"worker-1"}, true, true},
		{"node name differs", "metadata.name", corev1.NodeSelectorOpIn, []string{"worker-2"}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			requirement := corev1.NodeSelectorRequirement{Key: test.key, Operator: test.operator, Values: test.values}
			term := corev1.NodeSelectorTerm{}
			if test.field {
				term.MatchFields = []corev1.NodeSelectorRequirement{requirement}
			} else {
				term.MatchExpressions = []corev1.NodeSelectorRequirement{requirement}
			}
			spec := &corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{term},
				},
			}}}
			assert.Equal(t, test.want, nodeMatchesPodSpec(node, spec))
		})
	}
	assert.False(t, nodeMatchesPodSpec(node, &corev1.PodSpec{NodeSelector: map[string]string{"os": "windows"}}))
}

func TestNodeStatusSchedulingTolerations(t *testing.T) {
	for _, test := range []struct {
		name       string
		effect     corev1.TaintEffect
		toleration *corev1.Toleration
		want       bool
	}{
		{"untolerated NoSchedule", corev1.TaintEffectNoSchedule, nil, false},
		{"untolerated NoExecute", corev1.TaintEffectNoExecute, nil, false},
		{"soft taint", corev1.TaintEffectPreferNoSchedule, nil, true},
		{"exact value", corev1.TaintEffectNoSchedule, &corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "storage"}, true},
		{"different value", corev1.TaintEffectNoSchedule, &corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "other"}, false},
		{"different key", corev1.TaintEffectNoSchedule, &corev1.Toleration{Key: "other", Operator: corev1.TolerationOpExists}, false},
		{"different effect", corev1.TaintEffectNoExecute, &corev1.Toleration{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, false},
		{"key exists", corev1.TaintEffectNoExecute, &corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists}, true},
		{"all taints", corev1.TaintEffectNoSchedule, &corev1.Toleration{Operator: corev1.TolerationOpExists}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := &corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{{
				Key: "dedicated", Value: "storage", Effect: test.effect,
			}}}}
			spec := &corev1.PodSpec{}
			if test.toleration != nil {
				spec.Tolerations = []corev1.Toleration{*test.toleration}
			}
			assert.Equal(t, test.want, nodeMatchesPodSpec(node, spec))
		})
	}
}
