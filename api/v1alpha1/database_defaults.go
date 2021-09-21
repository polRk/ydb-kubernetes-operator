package v1alpha1

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
)

// SetDatabaseSpecDefaults sets various values to the
// default vars.
func SetDatabaseSpecDefaults(ydbCr *Database, ydbSpec *DatabaseSpec) {
	if ydbSpec.StorageClusterRef.Namespace == "" {
		ydbSpec.StorageClusterRef.Namespace = ydbCr.Namespace
	}

	if ydbSpec.Image.Name == "" {
		if ydbSpec.YDBVersion == "" {
			ydbSpec.Image.Name = fmt.Sprintf(ImagePathFormat, RegistryPath, DefaultTag)
		} else {
			ydbSpec.Image.Name = fmt.Sprintf(ImagePathFormat, RegistryPath, ydbSpec.YDBVersion)
		}
	}

	if ydbSpec.Image.PullPolicyName == nil {
		policy := v1.PullIfNotPresent
		ydbSpec.Image.PullPolicyName = &policy
	}
}