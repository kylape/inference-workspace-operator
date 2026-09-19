package platform

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeDiscovery struct {
	groups *metav1.APIGroupList
	err    error
}

func (f fakeDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	return f.groups, f.err
}

func TestIsOpenShift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		groups []metav1.APIGroup
		want   bool
	}{
		{name: "OpenShift", groups: []metav1.APIGroup{{Name: openShiftSecurityAPIGroup}}, want: true},
		{name: "Kubernetes", groups: []metav1.APIGroup{{Name: "apps"}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := IsOpenShift(context.Background(), fakeDiscovery{groups: &metav1.APIGroupList{Groups: test.groups}})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("IsOpenShift() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsOpenShiftReturnsDiscoveryError(t *testing.T) {
	t.Parallel()
	want := errors.New("discovery failed")
	_, err := IsOpenShift(context.Background(), fakeDiscovery{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("IsOpenShift() error = %v, want %v", err, want)
	}
}
