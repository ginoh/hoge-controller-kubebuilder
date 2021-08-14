/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	samplecontrollerv1alpha1 "github.com/ginoh/hoge-controller-kubebuilder/api/v1alpha1"
)

// HogeReconciler reconciles a Hoge object
type HogeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=samplecontroller.example.com,resources=hoges,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=samplecontroller.example.com,resources=hoges/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=samplecontroller.example.com,resources=hoges/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Hoge object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile

// +kubebuilder:rbac:groups=samplecontroller.example.com,resources=hoges,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=samplecontroller.example.com,resources=hoges/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *HogeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var hoge samplecontrollerv1alpha1.Hoge
	logger.Info("fetching Hoge Resource")
	if err := r.Get(ctx, req.NamespacedName, &hoge); err != nil {
		logger.Error(err, "unable to fetch Hoge")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.cleanupOwnedResources(ctx, &hoge); err != nil {
		logger.Error(err, "failed to clean up old Deployment resources for this Hoge")
		return ctrl.Result{}, err
	}

	deploymentName := hoge.Spec.DeploymentName
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: req.Namespace,
		},
	}

	if _, err := ctrl.CreateOrUpdate(ctx, r.Client, deploy, func() error {

		// set the replicas from hoge.Spec
		replicas := int32(1)
		if hoge.Spec.Replicas != nil {
			replicas = *hoge.Spec.Replicas
		}
		deploy.Spec.Replicas = &replicas

		labels := map[string]string{
			"app":        "nginx",
			"controller": req.Name,
		}

		// set labels to spec.selector for our deployment
		if deploy.Spec.Selector == nil {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}

		// set labels to template.objectMeta for our deployment
		if deploy.Spec.Template.ObjectMeta.Labels == nil {
			deploy.Spec.Template.ObjectMeta.Labels = labels
		}

		containers := []corev1.Container{
			{
				Name:  "nginx",
				Image: "nginx:latest",
			},
		}

		// set containers to template.spec.containers for our deployment
		if deploy.Spec.Template.Spec.Containers == nil {
			deploy.Spec.Template.Spec.Containers = containers
		}

		// set the owner so that garbage collection can kicks in
		if err := ctrl.SetControllerReference(&hoge, deploy, r.Scheme); err != nil {
			logger.Error(err, "unable to set ownerReference from Hoge to Deployment")
			return err
		}

		// end of ctrl.CreateOrUpdate
		return nil

	}); err != nil {

		// error handling of ctrl.CreateOrUpdate
		logger.Error(err, "unable to ensure deployment is correct")
		return ctrl.Result{}, err

	}

	// get deployment object from in-memory-cache
	var deployment appsv1.Deployment
	var deploymentNamespacedName = client.ObjectKey{Namespace: req.Namespace, Name: hoge.Spec.DeploymentName}
	if err := r.Get(ctx, deploymentNamespacedName, &deployment); err != nil {
		logger.Error(err, "unable to fetch Deployment")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// set hoge.status.AvailableReplicas from deployment
	availableReplicas := deployment.Status.AvailableReplicas
	if availableReplicas == hoge.Status.AvailableReplicas {
		// if availableReplicas equals availableReplicas, we wouldn't update anything.
		// exit Reconcile func without updating hoge.status
		return ctrl.Result{}, nil
	}
	hoge.Status.AvailableReplicas = availableReplicas

	// update hoge.status
	if err := r.Status().Update(ctx, &hoge); err != nil {
		logger.Error(err, "unable to update Hoge status")
		return ctrl.Result{}, err
	}

	// create event for updated hoge.status
	r.Recorder.Eventf(&hoge, corev1.EventTypeNormal, "Updated", "Update hoge.status.AvailableReplicas: %d", hoge.Status.AvailableReplicas)

	return ctrl.Result{}, nil
}

// cleanupOwnedResources will delete any existing Deployment resources that
// were created for the given Hoge that no longer match the
// hoge.spec.deploymentName field.
func (r *HogeReconciler) cleanupOwnedResources(ctx context.Context, hoge *samplecontrollerv1alpha1.Hoge) error {
	logger := log.FromContext(ctx)

	logger.Info("finding existing Deployments for Hoge resource")

	// List all deployment resources owned by this Hoge
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(hoge.Namespace), client.MatchingFields(map[string]string{deploymentOwnerKey: hoge.Name})); err != nil {
		return err
	}

	// Delete deployment if the deployment name doesn't match hoge.spec.deploymentName
	for _, deployment := range deployments.Items {
		if deployment.Name == hoge.Spec.DeploymentName {
			continue
		}

		// Delete old deployment object which doesn't match hoge.spec.deploymentName
		if err := r.Delete(ctx, &deployment); err != nil {
			logger.Error(err, "failed to delete Deployment resource")
			return err
		}

		logger.Info("delete deployment resource: " + deployment.Name)
		r.Recorder.Eventf(hoge, corev1.EventTypeNormal, "Deleted", "Deleted deployment %q", deployment.Name)
	}

	return nil
}

var (
	deploymentOwnerKey = ".metadata.controller"
	apiGVStr           = samplecontrollerv1alpha1.GroupVersion.String()
)

// SetupWithManager sets up the controller with the Manager.
func (r *HogeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	// add deploymentOwnerKey index to deployment object which hoge resource owns
	if err := mgr.GetFieldIndexer().IndexField(ctx, &appsv1.Deployment{}, deploymentOwnerKey, func(rawObj client.Object) []string {
		// grab the deployment object, extract the owner...
		deployment := rawObj.(*appsv1.Deployment)
		owner := metav1.GetControllerOf(deployment)
		if owner == nil {
			return nil
		}

		if owner.APIVersion != apiGVStr || owner.Kind != "Hoge" {
			return nil
		}

		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&samplecontrollerv1alpha1.Hoge{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
