package constants

const (
	AnnotationSkipImport           = "loft.sh/vcluster-skip-import"
	AnnotationUninstallOnDelete    = "loft.sh/uninstall-on-cluster-delete"
	NoRancherProjectOnNameSpace    = "no-rancher-project-on-namespace"
	LabelProjectUID                = "loft.sh/vcluster-project-uid"
	LabelProjectName               = "loft.sh/vcluster-project"
	LabelHostClusterName           = "loft.sh/vcluster-host-cluster"
	LabelVClusterServiceUID        = "loft.sh/vcluster-service-uid"
	LabelParentRoleTemplateBinding = "loft.sh/parent-prtb"
	LabelChildRoleTemplateBinding  = "loft.sh/parent-crtb"
	LabelTargetApp                 = "loft.sh/target-app"
	LabelRancherSystemToken        = "loft.sh/vcluster-rancher-system-token"
	LabelVClusterRancherUser       = "loft.sh/vcluster-rancher-user"
	FinalizerVClusterApp           = "loft.sh/vcluster-app-cleanup"
)
