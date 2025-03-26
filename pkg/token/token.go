package token

import (
	"bytes"
	"context"
	"fmt"

	"github.com/loft-sh/vcluster-rancher-operator/pkg/rancher"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured"
	"github.com/rancher/wrangler/pkg/randomtoken"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type data struct {
	ClusterName string
	Host        string
	ClusterID   string
	Token       string
}

func GetToken(ctx context.Context, client unstructured.Client) (string, error) {
	tokenList, err := client.ListWithLabel(ctx, schema.GroupVersionKind{Kind: "token", Group: "management.cattle.io", Version: "v3"}, "loft.sh/vcluster-rancher-system-token", "true")
	if err != nil {
		return "", err
	}

	for index := range tokenList.Items {
		err = client.Delete(ctx, &tokenList.Items[index])
		if err != nil {
			return "", err
		}
	}

	systemUser, err := client.GetFirstWithLabel(ctx, schema.GroupVersionKind{Kind: "User", Group: "management.cattle.io", Version: "v3"}, "loft.sh/vcluster-rancher-user", "true")
	if err != nil {
		return "", err
	}

	tokenValue, err := randomtoken.Generate()
	if err != nil {
		return "", err
	}

	_, err = client.Create(
		ctx,
		schema.GroupVersionKind{Kind: "Token", Group: "management.cattle.io", Version: "v3"},
		"token-vcluster-rancher-operator",
		"",
		false,
		map[string]string{"loft.sh/vcluster-rancher-system-token": "true"},
		nil,
		map[string]interface{}{
			"authProvider": "local",
			"userId":       systemUser.GetName(),
			"token":        tokenValue,
		})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("token-vcluster-rancher-operator:%s", tokenValue), nil
}

func RestConfigFromToken(clusterID, token string) (*rest.Config, error) {
	kubeConfig, err := ForTokenBased(clusterID, rancher.GetClusterEndpoint(clusterID), token)
	if err != nil {
		return nil, err
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeConfig))
	if err != nil {
		return nil, err
	}
	restConfig.Insecure = true

	return restConfig, nil
}

func ForTokenBased(clusterID, host, token string) (string, error) {
	// this code is mostly taken from rancher/wrangler
	data := &data{
		ClusterName: "",
		ClusterID:   clusterID,
		Host:        host,
		Token:       token,
	}

	if data.ClusterName == "" {
		data.ClusterName = data.ClusterID
	}

	buf := &bytes.Buffer{}
	err := tokenTemplate.Execute(buf, data)
	return buf.String(), err
}
