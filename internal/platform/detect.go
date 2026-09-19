package platform

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const openShiftSecurityAPIGroup = "security.openshift.io"

type groupDiscovery interface {
	ServerGroups() (*metav1.APIGroupList, error)
}

// IsOpenShift identifies OpenShift through API discovery. Discovery failures are
// returned rather than silently selecting a potentially incompatible profile.
func IsOpenShift(_ context.Context, discovery groupDiscovery) (bool, error) {
	groups, err := discovery.ServerGroups()
	if err != nil {
		return false, err
	}
	for _, group := range groups.Groups {
		if group.Name == openShiftSecurityAPIGroup {
			return true, nil
		}
	}
	return false, nil
}
