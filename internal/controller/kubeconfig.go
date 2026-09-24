package controller

import (
	"encoding/base64"
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

func buildServiceAccountKubeconfig(server string, caData, token []byte, clusterName, userName string) ([]byte, error) {
	if server == "" {
		return nil, fmt.Errorf("kubernetes API server URL is empty")
	}
	if len(caData) == 0 {
		return nil, fmt.Errorf("kubernetes CA data is empty")
	}
	if len(token) == 0 {
		return nil, fmt.Errorf("service account token is empty")
	}
	config := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []interface{}{
			map[string]interface{}{
				"name": clusterName,
				"cluster": map[string]interface{}{
					"server":                     server,
					"certificate-authority-data": base64.StdEncoding.EncodeToString(caData),
				},
			},
		},
		"users": []interface{}{
			map[string]interface{}{
				"name": userName,
				"user": map[string]interface{}{"token": string(token)},
			},
		},
		"contexts": []interface{}{
			map[string]interface{}{
				"name": clusterName,
				"context": map[string]interface{}{
					"cluster": clusterName,
					"user":    userName,
				},
			},
		},
		"current-context": clusterName,
	}
	return yaml.Marshal(config)
}
