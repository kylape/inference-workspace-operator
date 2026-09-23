package controller

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

func rewriteKubeconfigServer(raw []byte, server string) ([]byte, error) {
	config := map[string]interface{}{}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return nil, err
	}

	clusters, found := config["clusters"].([]interface{})
	if !found || len(clusters) == 0 {
		return nil, fmt.Errorf("kubeconfig has no clusters")
	}
	updated := false
	for _, item := range clusters {
		clusterEntry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		cluster, ok := clusterEntry["cluster"].(map[string]interface{})
		if !ok {
			continue
		}
		cluster["server"] = server
		updated = true
	}
	if !updated {
		return nil, fmt.Errorf("kubeconfig has no cluster entries")
	}

	return yaml.Marshal(config)
}
