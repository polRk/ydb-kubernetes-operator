package storage

import (
	"context"
	"fmt"
	"time"

	ydbv1alpha1 "github.com/ydb-platform/ydb-kubernetes-operator/api/v1alpha1"
	"github.com/ydb-platform/ydb-kubernetes-operator/controllers"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/exec"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/healthcheck"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/labels"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/resources"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	StorageInitializedCondition = "StorageInitialized"
)

func (r *StorageReconciler) Sync(ctx context.Context, cr *ydbv1alpha1.Storage) (ctrl.Result, error) {
	var err error
	var result ctrl.Result

	storage := resources.NewCluster(cr)
	storage.SetStatusOnFirstReconcile()

	// wait for resources

	result, err = r.waitForStatefulSetToScale(ctx, &storage)
	if err != nil || !result.IsZero() {
		return result, err
	}

	// create resources

	result, err = r.handleResourcesSync(ctx, &storage)
	if err != nil || !result.IsZero() {
		return result, err
	}

	// do init

	result, err = r.waitForHealthCheck(ctx, &storage)
	if err != nil || !result.IsZero() {
		return result, err
	}

	result, err = r.runDefineBoxScript(ctx, &storage)
	if err != nil || !result.IsZero() {
		return result, err
	}

	// set ready status

	return controllers.Ok()
}

func (r *StorageReconciler) runDefineBoxScript(ctx context.Context, storage *resources.StorageClusterBuilder) (ctrl.Result, error) {
	if meta.IsStatusConditionTrue(storage.Status.Conditions, StorageInitializedCondition) {
		return controllers.Ok()
	}

	podName := fmt.Sprintf("%s-0", storage.Name)

	cmd := []string{
		"/opt/kikimr/bin/kikimr",
		"admin",
		"bs",
		"config",
		"invoke",
		"--proto-file",
		"/opt/kikimr/cfg/DefineBox.txt",
	}

	stdout, stderr, err := exec.ExecInPod(r.Scheme, r.Config, storage.Namespace, podName, "ydb-storage", cmd)

	if err != nil {
		fmt.Println(stdout)
		fmt.Println(stderr)
		return controllers.RequeueAfter(30*time.Second, err)
	}

	cmd = []string{
		"/opt/kikimr/bin/kikimr",
		"admin",
		"console",
		"execute",
		"--domain=Root",
		"--retry=10",
		"/opt/kikimr/cfg/ConfigureRoot.txt",
	}

	stdout, stderr, err = exec.ExecInPod(r.Scheme, r.Config, storage.Namespace, podName, "ydb-storage", cmd)

	if err != nil {
		fmt.Println(stdout)
		fmt.Println(stderr)
		return controllers.RequeueAfter(30*time.Second, err)
	}

	resourcesProvided := metav1.Condition{
		Type:    StorageInitializedCondition,
		Status:  "True",
		Reason:  "StorageInitialized",
		Message: "Storage initialized successfully",
	}
	meta.SetStatusCondition(&storage.Status.Conditions, resourcesProvided)
	if _, err := r.setState(ctx, storage); err != nil {
		return controllers.NoRequeue(err)
	}

	return controllers.RequeueImmediately()
}

func (r *StorageReconciler) waitForStatefulSetToScale(ctx context.Context, storage *resources.StorageClusterBuilder) (ctrl.Result, error) {
	found := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      storage.Name,
		Namespace: storage.Namespace,
	}, found)

	if err != nil && errors.IsNotFound(err) {
		return controllers.Ok()
	} else if err != nil {
		r.Recorder.Event(
			storage,
			corev1.EventTypeNormal,
			"Syncing",
			fmt.Sprintf("Failed to get StatefulSets: %s", err),
		)
		return controllers.NoRequeue(err)
	}

	podLabels := labels.Common(storage.Name, make(map[string]string))
	podLabels.Merge(map[string]string{
		labels.ComponentKey: labels.StorageComponent,
	})

	matchingLabels := client.MatchingLabels{}
	for k, v := range podLabels {
		matchingLabels[k] = v
	}

	podList := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(storage.Namespace),
		matchingLabels,
	}
	err = r.List(ctx, podList, opts...)

	if err != nil {
		r.Recorder.Event(
			storage,
			corev1.EventTypeNormal,
			"Syncing",
			fmt.Sprintf("Failed to list cluster pods: %s", err),
		)
		return controllers.NoRequeue(err)
	}

	runningPods := 0
	for _, e := range podList.Items {
		if e.Status.Phase == "Running" {
			runningPods += 1
		}
	}

	if runningPods != int(storage.Spec.Nodes) {
		storage.Status.State = "Provisioning"
		if _, err := r.setState(ctx, storage); err != nil {
			return controllers.NoRequeue(err)
		}

		msg := fmt.Sprintf("Waiting for number of running pods to match expected: %d != %d", runningPods, storage.Spec.Nodes)
		r.Recorder.Event(storage, corev1.EventTypeNormal, "Provisioning", msg)

		return controllers.RequeueAfter(10*time.Second, nil)
	}

	if storage.Status.State != "Ready" && meta.IsStatusConditionTrue(storage.Status.Conditions, StorageInitializedCondition) {
		storage.Status.State = "Ready"
		if _, err = r.setState(ctx, storage); err != nil {
			return controllers.NoRequeue(err)
		}
		r.Recorder.Event(storage, corev1.EventTypeNormal, "ResourcesReady", "Everything should be in sync")
	}

	return controllers.Ok()
}

func (r *StorageReconciler) waitForHealthCheck(ctx context.Context, storage *resources.StorageClusterBuilder) (ctrl.Result, error) {
	err := healthcheck.CheckBootstrapHealth(ctx, storage)

	if err != nil {
		r.Recorder.Event(
			storage,
			corev1.EventTypeNormal,
			"HealthcheckInProgress",
			fmt.Sprintf("Waiting for healthcheck, current status: %s", err),
		)
		return controllers.RequeueAfter(30*time.Second, err)
	}

	r.Recorder.Event(
		storage,
		corev1.EventTypeNormal,
		"HealthcheckOK",
		"Bootstrap healthcheck is green",
	)
	return controllers.Ok()
}

func (r *StorageReconciler) handleResourcesSync(ctx context.Context, storage *resources.StorageClusterBuilder) (ctrl.Result, error) {
	r.Recorder.Event(storage, corev1.EventTypeNormal, "Provisioning", "Resource sync is in progress")

	areResourcesCreated := false

	for _, builder := range storage.GetResourceBuilders() {
		rr := builder.Placeholder(storage)

		result, err := ctrl.CreateOrUpdate(ctx, r.Client, rr, func() error {
			err := builder.Build(rr)

			if err != nil {
				r.Recorder.Event(
					storage,
					corev1.EventTypeWarning,
					"ProvisioningFailed",
					fmt.Sprintf("Failed building resources: %s", err),
				)
				return err
			}

			err = ctrl.SetControllerReference(storage.Unwrap(), rr, r.Scheme)
			if err != nil {
				r.Recorder.Event(
					storage,
					corev1.EventTypeWarning,
					"ProvisioningFailed",
					fmt.Sprintf("Error setting controller reference for resource: %s", err),
				)
				return err
			}

			return nil
		})

		if err != nil {
			r.Recorder.Event(
				storage,
				corev1.EventTypeWarning,
				"ProvisioningFailed",
				fmt.Sprintf("Failed syncing resources: %s", err),
			)
			return controllers.NoRequeue(err)
		}

		areResourcesCreated = areResourcesCreated || (result == controllerutil.OperationResultCreated)
	}

	r.Recorder.Event(storage, corev1.EventTypeNormal, "Provisioning", "Resource sync complete")

	if areResourcesCreated {
		return controllers.RequeueImmediately()
	}

	return controllers.Ok()
}

func (r *StorageReconciler) setState(ctx context.Context, storage *resources.StorageClusterBuilder) (ctrl.Result, error) {
	storageCr := &ydbv1alpha1.Storage{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: storage.Namespace,
		Name:      storage.Name,
	}, storageCr)

	if err != nil {
		r.Recorder.Event(storageCr, corev1.EventTypeWarning, "ControllerError", "Failed fetching CR before status update")
		return controllers.NoRequeue(err)
	}

	storageCr.Status.State = storage.Status.State
	storageCr.Status.Conditions = storage.Status.Conditions

	err = r.Status().Update(ctx, storageCr)
	if err != nil {
		r.Recorder.Event(storageCr, corev1.EventTypeWarning, "ControllerError", fmt.Sprintf("Failed setting status: %s", err))
		return controllers.NoRequeue(err)
	}

	return controllers.Ok()
}