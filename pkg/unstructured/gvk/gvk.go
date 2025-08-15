package gvk

import "k8s.io/apimachinery/pkg/runtime/schema"

var (
	ClustersManagementCattle                   = schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Cluster"}
	ProjectManagementCattle                    = schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Project"}
	TokenManagementCattle                      = schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Token"}
	UserManagementCattle                       = schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "User"}
	ProjectRoleTemplateBindingManagementCattle = schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ProjectRoleTemplateBinding"}
	ClusterRoleTemplateBindingManagementCattle = schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRoleTemplateBinding"}
	ClusterRegistrationTokenManagementCattle   = schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRegistrationToken"}
	ClusterProvisioningCattle                  = schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}
)
