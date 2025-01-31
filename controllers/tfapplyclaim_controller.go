/*


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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	claimv1alpha1 "github.com/tmax-cloud/tfc-operator/api/v1alpha1"
	"github.com/tmax-cloud/tfc-operator/util"
)

// TFApplyClaimReconciler reconciles a TFApplyClaim object
type TFApplyClaimReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=claim.tmax.io,resources=tfapplyclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claim.tmax.io,resources=tfapplyclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=claim.tmax.io,resources=tfapplyclaims/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create

func (r *TFApplyClaimReconciler) Reconcile(req ctrl.Request) (_ ctrl.Result, reterr error) {
	ctx := context.Background()
	log := r.Log.WithValues("tfapplyclaim", req.NamespacedName)

	// Fetch the "TFApplyClaim" instance
	tfapplyclaim := &claimv1alpha1.TFApplyClaim{}
	err := r.Get(ctx, req.NamespacedName, tfapplyclaim)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("TFApplyClaim resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get TFApplyClaim")
		return ctrl.Result{}, err
	}
	helper, _ := patch.NewHelper(tfapplyclaim, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	defer func() {
		if err := helper.Patch(ctx, tfapplyclaim); err != nil {
			reterr = err
		}
	}()
	return r.reconcile(context.TODO(), tfapplyclaim)
}

func (r *TFApplyClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {

	/* FieldIndexer를 통해 status.phase 필드 인덱스를 캐시에 포함 (for MatchingFields) */
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "status.phase", func(rawObj runtime.Object) []string {
		pod := rawObj.(*corev1.Pod)
		return []string{string(pod.Status.DeepCopy().Phase)}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&claimv1alpha1.TFApplyClaim{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

// reconcile handles cluster reconciliation.
func (r *TFApplyClaimReconciler) reconcile(ctx context.Context, tfapplyclaim *claimv1alpha1.TFApplyClaim) (ctrl.Result, error) {
	phases := []func(context.Context, *claimv1alpha1.TFApplyClaim) (ctrl.Result, error){}
	action := tfapplyclaim.Status.Action

	// 공통적으로 수행
	phases = append(
		phases,
		r.ReadyClaim,
	)

	if action == "Approve" || action == "Reject" {
		phases = append(phases, r.ApproveClaim)
	} else if action == "Plan" {
		phases = append(phases, r.PlanClaim)
	} else if action == "Apply" {
		phases = append(phases, r.ApplyClaim)
	} else if tfapplyclaim.Spec.Destroy == true {
		phases = append(phases, r.DestroyClaim)
	}

	res := ctrl.Result{}
	errs := []error{}
	// phases 를 돌면서, append 한 함수들을 순차적으로 수행하고,
	// 다시 requeue 가 되어야 하는 경우, LowestNonZeroResult 함수를 통해 requeueAfter time 이 가장 짧은 함수를 찾는다.
	for _, phase := range phases {
		// Call the inner reconciliation methods.
		phaseResult, err := phase(ctx, tfapplyclaim)
		if err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			continue
		}

		// Aggregate phases which requeued without err
		res = util.LowestNonZeroResult(res, phaseResult)
	}

	return res, kerrors.NewAggregate(errs)
}
