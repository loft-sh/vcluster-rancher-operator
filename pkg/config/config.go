package config

import (
	"os"
	"strings"

	"github.com/loft-sh/vcluster-rancher-operator/pkg/constants"
)

type Config struct {
	FleetDefaultWorkspace                 string
	FleetProjectUIDToWorkspaceMappings    map[string]string
	FleetAutoCreateWorkspace              bool
	ServiceSyncIncludeLabelKeys           []string
	ServiceSyncExcludeLabelKeys           []string
	ServiceSyncIncludeLabelKeysWithPrefix []string
	ServiceSyncExcludeLabelKeysWithPrefix []string
}

func LoadConfigFromEnv() Config {
	config := Config{}
	config.FleetDefaultWorkspace = os.Getenv(constants.EnvFleetDefaultWorkspace)
	config.FleetAutoCreateWorkspace = os.Getenv(constants.EnvFleetAutoCreateWorkspace) == "true"
	for _, v := range strings.Split(os.Getenv(constants.EnvFleetProjectUIDWorkspaceMapping), ",") {
		parts := strings.Split(v, ":")
		if len(parts) != 2 {
			continue
		}
		if config.FleetProjectUIDToWorkspaceMappings == nil {
			config.FleetProjectUIDToWorkspaceMappings = make(map[string]string)
		}
		config.FleetProjectUIDToWorkspaceMappings[parts[0]] = parts[1]
	}

	for _, v := range strings.Split(os.Getenv(constants.EnvSyncIncludeLabelKeys), ",") {
		if strings.HasSuffix(v, "*") {
			config.ServiceSyncIncludeLabelKeysWithPrefix = append(config.ServiceSyncIncludeLabelKeysWithPrefix, strings.TrimSuffix(v, "*"))
			continue
		}
		config.ServiceSyncIncludeLabelKeys = append(config.ServiceSyncIncludeLabelKeys, v)
	}

	for _, v := range strings.Split(os.Getenv(constants.EnvSyncExcludeLabelKeys), ",") {
		if strings.HasSuffix(v, "*") {
			config.ServiceSyncExcludeLabelKeysWithPrefix = append(config.ServiceSyncExcludeLabelKeysWithPrefix, strings.TrimSuffix(v, "*"))
			continue
		}
		config.ServiceSyncExcludeLabelKeys = append(config.ServiceSyncExcludeLabelKeys, v)
	}

	return config
}
