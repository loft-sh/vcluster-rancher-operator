package rancher

import "fmt"

const (
	clusterEndpointFmt = "https://rancher.cattle-system/k8s/clusters/%s"
)

func GetClusterEndpoint(clusterID string) string {
	return fmt.Sprintf(clusterEndpointFmt, clusterID)
}
