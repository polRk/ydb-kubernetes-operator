package resources

import (
	"fmt"

	api "github.com/ydb-platform/ydb-kubernetes-operator/api/v1alpha1"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/configuration"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/labels"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/metrics"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StorageClusterBuilder struct {
	*api.Storage
}

func NewCluster(ydbCr *api.Storage) StorageClusterBuilder {
	cr := ydbCr.DeepCopy()

	api.SetStorageClusterSpecDefaults(&cr.Spec)

	return StorageClusterBuilder{cr}
}

func (b *StorageClusterBuilder) SetStatusOnFirstReconcile() {
	if b.Status.Conditions == nil {
		b.Status.Conditions = []metav1.Condition{}
	}
}

func (b *StorageClusterBuilder) Unwrap() *api.Storage {
	return b.DeepCopy()
}

func (b *StorageClusterBuilder) GetEndpoint() string {
	host := fmt.Sprintf("%s-grpc.%s.svc.cluster.local", b.Name, b.Namespace)

	return fmt.Sprintf("%s:%d", host, api.GRPCPort)
}

func (b *StorageClusterBuilder) GetResourceBuilders() []ResourceBuilder {
	ll := labels.ClusterLabels(b.Unwrap())

	cfg, _ := configuration.Build(b.Unwrap())

	serviceMonitors := make([]ResourceBuilder, len(metrics.GetStorageMetricEndpoints()))

	for i, e := range metrics.GetStorageMetricEndpoints() {
		smLabels := ll.Copy()
		serviceMonitors[i] = &ServiceMonitorBuilder{
			Object: b,
			Name:   fmt.Sprintf("%s-%s", b.Name, e.MonitorName),
			Labels: smLabels,
		}
	}

	return append(
		serviceMonitors,
		&ServiceBuilder{
			Object:     b,
			Labels:     ll,
			NameFormat: grpcServiceNameFormat,
			Ports: []corev1.ServicePort{{
				Name: "grpc",
				Port: api.GRPCPort,
			}}},
		&ServiceBuilder{
			Object:     b,
			Labels:     ll,
			Headless:   true,
			NameFormat: interconnectServiceNameFormat,
			Ports: []corev1.ServicePort{{
				Name: "interconnect",
				Port: api.InterconnectPort,
			}}},
		&ServiceBuilder{
			Object:     b,
			Labels:     ll,
			NameFormat: statusServiceNameFormat,
			Ports: []corev1.ServicePort{{
				Name: "status",
				Port: api.StatusPort,
			}}},
		&ConfigMapBuilder{
			Object: b,
			Data:   cfg,
			Labels: ll,
		},
		&StorageStatefulSetBuilder{Storage: b.Unwrap(), Labels: ll},
	)
}