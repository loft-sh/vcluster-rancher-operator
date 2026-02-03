package constants

const (

	// legacy annotations

	AnnotationSkipImport           = "loft.sh/vcluster-skip-import"
	AnnotationUninstallOnDelete    = "loft.sh/uninstall-on-cluster-delete"
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

	// going forward, we will use the "platform.vcluster.com" prefix

	AnnotationCustomName       = "platform.vcluster.com/custom-name"
	AnnotationCustomNamePrefix = "platform.vcluster.com/custom-name-prefix"

	ValueNoRancherProjectOnNameSpace = "no-rancher-project-on-namespace"
)
