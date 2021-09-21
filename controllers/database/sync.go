package database

import (
	"context"
	"fmt"
	"time"

	ydbv1alpha1 "github.com/ydb-platform/ydb-kubernetes-operator/api/v1alpha1"
	"github.com/ydb-platform/ydb-kubernetes-operator/controllers"
	"github.com/ydb-platform/ydb-kubernetes-operator/pkg/cms"
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
	Provisioning ClusterState = "Provisioning"
	Initializing ClusterState = "Initializing"
	Ready        ClusterState = "Ready"

	DefaultRequeueDelay        = 10 * time.Second
	TenantCreationRequeueDelay = 30 * time.Second
	StorageAwaitRequeueDelay   = 60 * time.Second

	ConditionTenantInitialized = "TenantInitialized"
)

type ClusterState string

func (r *DatabaseReconciler) Sync(ctx context.Context, ydbCr *ydbv1alpha1.Database) (ctrl.Result, error) {
	var err error
	var result ctrl.Result

	database := resources.NewDatabase(ydbCr)
	database.SetStatusOnFirstReconcile()
	_, err = r.setState(ctx, &database)

	result, err = r.waitForClusterResource(ctx, &database)
	if err != nil || !result.IsZero() {
		return result, err
	}

	result, err = r.waitForStatefulSetToScale(ctx, &database)
	if err != nil || !result.IsZero() {
		return result, err
	}

	result, err = r.handleResourcesSync(ctx, &database)
	if err != nil || !result.IsZero() {
		return result, err
	}

	if !meta.IsStatusConditionTrue(database.Status.Conditions, ConditionTenantInitialized) {
		result, err = r.handleTenantCreation(ctx, &database)
		if err != nil || !result.IsZero() {
			return result, err
		}
	}

	return controllers.Ok()
}

func (r *DatabaseReconciler) waitForClusterResource(ctx context.Context, database *resources.DatabaseBuilder) (ctrl.Result, error) {
	found := &ydbv1alpha1.Storage{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      database.Spec.StorageClusterRef.Name,
		Namespace: database.Spec.StorageClusterRef.Namespace,
	}, found)

	if err != nil {
		r.Recorder.Event(
			database,
			corev1.EventTypeWarning,
			"Pending",
			fmt.Sprintf(
				"Failed to get (%s, %s) resource of type cluster.ydb.tech: %s",
				database.Spec.StorageClusterRef.Name,
				database.Spec.StorageClusterRef.Namespace,
				err,
			),
		)
		return controllers.RequeueAfter(StorageAwaitRequeueDelay, err)
	}

	if found.Status.State != "Ready" {
		r.Recorder.Event(
			database,
			corev1.EventTypeWarning,
			"Pending",
			fmt.Sprintf(
				"Referenced storage cluster (%s, %s) in a bad state: %s != Ready",
				database.Spec.StorageClusterRef.Name,
				database.Spec.StorageClusterRef.Namespace,
				found.Status.State,
			),
		)
		return controllers.RequeueAfter(StorageAwaitRequeueDelay, err)
	}

	return controllers.Ok()
}

func (r *DatabaseReconciler) waitForStatefulSetToScale(ctx context.Context, database *resources.DatabaseBuilder) (ctrl.Result, error) {
	found := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      database.Name,
		Namespace: database.Namespace,
	}, found)

	if err != nil && errors.IsNotFound(err) {
		return controllers.Ok()
	} else if err != nil {
		r.Recorder.Event(
			database,
			corev1.EventTypeNormal,
			"Syncing",
			fmt.Sprintf("Failed to get StatefulSets: %s", err),
		)
		return controllers.NoRequeue(err)
	}

	if found.Status.Replicas != database.Spec.Nodes {
		database.Status.State = string(Provisioning)
		if _, err := r.setState(ctx, database); err != nil {
			return controllers.NoRequeue(err)
		}

		msg := fmt.Sprintf("Waiting for number of running pods to match expected: %d != %d", found.Status.Replicas, database.Spec.Nodes)
		r.Recorder.Event(database, corev1.EventTypeNormal, "Provisioning", msg)

		return controllers.RequeueAfter(DefaultRequeueDelay, nil)
	}

	if database.Status.State != string(Ready) && meta.IsStatusConditionTrue(database.Status.Conditions, ConditionTenantInitialized) {
		database.Status.State = string(Ready)
		if _, err = r.setState(ctx, database); err != nil {
			return controllers.NoRequeue(err)
		}
		r.Recorder.Event(database, corev1.EventTypeNormal, "ResourcesReady", "Resource are ready and DB is initialized")
	}

	return controllers.Ok()
}

func (r *DatabaseReconciler) handleResourcesSync(ctx context.Context, database *resources.DatabaseBuilder) (ctrl.Result, error) {
	r.Recorder.Event(database, corev1.EventTypeNormal, "Provisioning", "Resource sync is in progress")

	areResourcesCreated := false

	for _, builder := range database.GetResourceBuilders() {
		rr := builder.Placeholder(database)

		result, err := ctrl.CreateOrUpdate(ctx, r.Client, rr, func() error {
			err := builder.Build(rr)

			if err != nil {
				r.Recorder.Event(
					database,
					corev1.EventTypeWarning,
					"ProvisioningFailed",
					fmt.Sprintf("Failed building resources: %s", err),
				)
				return err
			}

			err = ctrl.SetControllerReference(database.Unwrap(), rr, r.Scheme)
			if err != nil {
				r.Recorder.Event(
					database,
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
				database,
				corev1.EventTypeWarning,
				"ProvisioningFailed",
				fmt.Sprintf("Failed syncing resources: %s", err),
			)
			return controllers.NoRequeue(err)
		}

		areResourcesCreated = areResourcesCreated || (result == controllerutil.OperationResultCreated)
	}

	r.Recorder.Event(database, corev1.EventTypeNormal, "Provisioning", "Resource sync complete")

	if areResourcesCreated {
		return controllers.RequeueImmediately()
	}

	return controllers.Ok()
}

func (r *DatabaseReconciler) handleTenantCreation(ctx context.Context, database *resources.DatabaseBuilder) (ctrl.Result, error) {
	database.Status.State = string(Initializing)
	if _, err := r.setState(ctx, database); err != nil {
		return controllers.NoRequeue(err)
	}

	tenant := cms.NewTenant(database.GetTenantName())
	err := tenant.Create(ctx, database)
	if err != nil {
		r.Recorder.Event(database, corev1.EventTypeWarning, "InitializingFailed", fmt.Sprintf("Error creating tenant %s: %s", tenant.Name, err))
		return controllers.RequeueAfter(TenantCreationRequeueDelay, err)
	}
	r.Recorder.Event(database, corev1.EventTypeNormal, "Initialized", fmt.Sprintf("Tenant %s created", tenant.Name))

	resourcesProvided := metav1.Condition{
		Type:    ConditionTenantInitialized,
		Status:  "True",
		Reason:  "TenantInitialized",
		Message: "Tenant creation is complete",
	}
	meta.SetStatusCondition(&database.Status.Conditions, resourcesProvided)
	if _, err := r.setState(ctx, database); err != nil {
		return controllers.NoRequeue(err)
	}

	return controllers.RequeueImmediately()
}

func (r *DatabaseReconciler) setState(ctx context.Context, database *resources.DatabaseBuilder) (ctrl.Result, error) {
	databaseCr := &ydbv1alpha1.Database{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: database.Namespace,
		Name:      database.Name,
	}, databaseCr)

	if err != nil {
		r.Recorder.Event(databaseCr, corev1.EventTypeWarning, "ControllerError", "Failed fetching CR before status update")
		return controllers.NoRequeue(err)
	}

	databaseCr.Status.State = database.Status.State
	databaseCr.Status.Conditions = database.Status.Conditions

	err = r.Status().Update(ctx, databaseCr)
	if err != nil {
		r.Recorder.Event(databaseCr, corev1.EventTypeWarning, "ControllerError", fmt.Sprintf("Failed setting status: %s", err))
		return controllers.NoRequeue(err)
	}

	return controllers.Ok()
}