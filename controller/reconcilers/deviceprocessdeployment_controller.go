package reconcilers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metameta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	fieldManagerName           = "deviceprocess-controller"
	deviceProcessDeploymentKey = "deviceprocessdeployment"
)

//+kubebuilder:rbac:groups=azure.com,resources=deviceprocessdeployments,verbs=get;list;watch
//+kubebuilder:rbac:groups=azure.com,resources=deviceprocessdeployments/status,verbs=get
//+kubebuilder:rbac:groups=azure.com,resources=deviceprocesses,verbs=get;list;watch;create;patch;delete
//+kubebuilder:rbac:groups=azure.com,resources=deviceprocesses/status,verbs=get
//+kubebuilder:rbac:groups=azure.com,resources=networkswitches,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// DeviceProcessDeploymentReconciler reconciles DeviceProcessDeployment objects into DeviceProcess instances.
type DeviceProcessDeploymentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// NewDeviceProcessDeploymentReconciler constructs a reconciler instance.
func NewDeviceProcessDeploymentReconciler(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) *DeviceProcessDeploymentReconciler {
	return &DeviceProcessDeploymentReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: recorder,
	}
}

// SetupWithManager wires the reconciler into the controller manager.
func (r *DeviceProcessDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.DeviceProcessDeployment{}).
		Owns(&apiv1alpha1.DeviceProcess{}).
		Complete(r)
}

// Reconcile ensures DeviceProcess objects exist for each targeted NetworkSwitch.
func (r *DeviceProcessDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("deviceprocessdeployment", req.NamespacedName)
	ctx = log.IntoContext(ctx, logger)

	var deployment apiv1alpha1.DeviceProcessDeployment
	if err := r.Get(ctx, req.NamespacedName, &deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	selector, err := metav1.LabelSelectorAsSelector(&deployment.Spec.Selector)
	if err != nil {
		return ctrl.Result{}, err
	}

	devices, err := r.listNetworkSwitches(ctx, deployment.Namespace, selector)
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("reconciling deployment", "matchedDevices", len(devices))

	desiredNames := make(map[string]struct{}, len(devices))
	createdCount := 0
	updatedCount := 0

	for i := range devices {
		device := devices[i]
		name := deviceProcessName(deployment.Name, device.GetName())
		desiredNames[name] = struct{}{}

		created, err := r.applyDeviceProcess(ctx, &deployment, &device, name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if created {
			createdCount++
		} else {
			updatedCount++
		}
	}

	deletedCount, err := r.cleanupStale(ctx, &deployment, desiredNames)
	if err != nil {
		return ctrl.Result{}, err
	}

	if createdCount > 0 {
		r.Recorder.Eventf(&deployment, corev1.EventTypeNormal, "CreatedDeviceProcess", "Created %d DeviceProcess object(s)", createdCount)
	}
	if deletedCount > 0 {
		r.Recorder.Eventf(&deployment, corev1.EventTypeNormal, "DeletedDeviceProcess", "Deleted %d stale DeviceProcess object(s)", deletedCount)
	}

	logger.Info("reconcile complete", "created", createdCount, "updated", updatedCount, "deleted", deletedCount)

	return ctrl.Result{}, nil
}

func (r *DeviceProcessDeploymentReconciler) applyDeviceProcess(ctx context.Context, deployment *apiv1alpha1.DeviceProcessDeployment, device *unstructured.Unstructured, name string) (bool, error) {
	key := types.NamespacedName{Name: name, Namespace: deployment.Namespace}
	var existing apiv1alpha1.DeviceProcess
	err := r.Get(ctx, key, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	created := apierrors.IsNotFound(err)

	desired := buildDesiredDeviceProcess(deployment, device, name)
	desired.SetResourceVersion("")

	if err := controllerutil.SetControllerReference(deployment, desired, r.Scheme); err != nil {
		return created, err
	}

	applyOpts := []client.PatchOption{client.FieldOwner(fieldManagerName)}
	if err := r.Patch(ctx, desired, client.Apply, applyOpts...); err != nil {
		if isApplyNotSupported(err) {
			return r.upsertWithoutSSA(ctx, desired, created)
		}
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
				return created, err
			}
			created = true
			if err := r.Patch(ctx, desired, client.Apply, applyOpts...); err != nil {
				if isApplyNotSupported(err) {
					return r.upsertWithoutSSA(ctx, desired, created)
				}
				return created, err
			}
			return created, nil
		}
		return created, err
	}

	return created, nil
}

func (r *DeviceProcessDeploymentReconciler) upsertWithoutSSA(ctx context.Context, desired *apiv1alpha1.DeviceProcess, created bool) (bool, error) {
	desired.SetResourceVersion("")
	desired.SetManagedFields(nil)

	if created {
		if err := r.Create(ctx, desired); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return created, err
			}
			created = false
		}
		return created, nil
	}

	var current apiv1alpha1.DeviceProcess
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	if err := r.Get(ctx, key, &current); err != nil {
		return created, err
	}

	current.Labels = desired.Labels
	current.Annotations = desired.Annotations
	current.Spec = desired.Spec
	current.OwnerReferences = desired.OwnerReferences

	if err := r.Update(ctx, &current); err != nil {
		return created, err
	}

	return created, nil
}

func isApplyNotSupported(err error) bool {
	return strings.Contains(err.Error(), "apply patches are not supported")
}

func (r *DeviceProcessDeploymentReconciler) cleanupStale(ctx context.Context, deployment *apiv1alpha1.DeviceProcessDeployment, desired map[string]struct{}) (int, error) {
	var processes apiv1alpha1.DeviceProcessList
	if err := r.List(ctx, &processes, client.InNamespace(deployment.Namespace), client.MatchingLabels{deviceProcessDeploymentKey: deployment.Name}); err != nil {
		return 0, err
	}

	deleted := 0
	for i := range processes.Items {
		process := &processes.Items[i]
		if _, ok := desired[process.Name]; ok {
			continue
		}
		if !metav1.IsControlledBy(process, deployment) {
			continue
		}
		if err := r.Delete(ctx, process); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return deleted, err
		}
		deleted++
	}

	return deleted, nil
}

func (r *DeviceProcessDeploymentReconciler) listNetworkSwitches(ctx context.Context, namespace string, selector labels.Selector) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	gvk := schema.GroupVersionKind{Group: "azure.com", Version: "v1alpha1", Kind: "NetworkSwitchList"}
	list.SetGroupVersionKind(gvk)

	opts := []client.ListOption{client.InNamespace(namespace)}
	if selector != nil {
		opts = append(opts, client.MatchingLabelsSelector{Selector: selector})
	}

	if err := r.List(ctx, list, opts...); err != nil {
		if metameta.IsNoMatchError(err) {
			log.FromContext(ctx).Info("device kind not installed; skipping reconciliation for this kind", "gvk", gvk.String())
			return nil, nil
		}
		return nil, err
	}

	return list.Items, nil
}

func buildDesiredDeviceProcess(deployment *apiv1alpha1.DeviceProcessDeployment, device *unstructured.Unstructured, name string) *apiv1alpha1.DeviceProcess {
	template := deployment.Spec.Template
	selector := deployment.Spec.Selector

	labels := mergeStringMaps(template.Metadata.Labels, map[string]string{
		"app":                      deployment.Name,
		deviceProcessDeploymentKey: deployment.Name,
	})
	labels = mergeStringMaps(labels, selectedDeviceLabels(device.GetLabels(), &selector))

	return &apiv1alpha1.DeviceProcess{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiv1alpha1.SchemeGroupVersion.String(),
			Kind:       "DeviceProcess",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   deployment.Namespace,
			Labels:      labels,
			Annotations: template.Metadata.Annotations,
		},
		Spec: apiv1alpha1.DeviceProcessSpec{
			DeviceRef: apiv1alpha1.DeviceRef{
				Kind: apiv1alpha1.DeviceRefKindNetworkSwitch,
				Name: device.GetName(),
			},
			Artifact:      template.Spec.Artifact,
			Execution:     template.Spec.Execution,
			RestartPolicy: template.Spec.RestartPolicy,
			HealthCheck:   template.Spec.HealthCheck,
		},
	}
}

func deviceProcessName(deploymentName, deviceName string) string {
	base := strings.ToLower(fmt.Sprintf("%s-%s", deploymentName, deviceName))
	if len(validation.IsDNS1123Subdomain(base)) == 0 && len(base) <= validation.DNS1123SubdomainMaxLength {
		return base
	}

	hash := sha1.Sum([]byte(deviceName))
	hashStr := hex.EncodeToString(hash[:])[:10]
	maxPrefixLen := validation.DNS1123SubdomainMaxLength - len(hashStr) - 1
	prefix := strings.ToLower(deploymentName)
	if len(prefix) > maxPrefixLen {
		prefix = prefix[:maxPrefixLen]
	}
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		prefix = "dpd"
	}

	return fmt.Sprintf("%s-%s", prefix, hashStr)
}

func selectorLabelKeys(selector *metav1.LabelSelector) sets.Set[string] {
	keys := sets.New[string]()
	if selector == nil {
		return keys
	}

	for k := range selector.MatchLabels {
		keys.Insert(k)
	}
	for _, expr := range selector.MatchExpressions {
		keys.Insert(expr.Key)
	}

	return keys
}

func selectedDeviceLabels(deviceLabels map[string]string, selector *metav1.LabelSelector) map[string]string {
	if len(deviceLabels) == 0 {
		return nil
	}

	keys := selectorLabelKeys(selector)
	for _, k := range []string{"role", "type", "rack"} {
		keys.Insert(k)
	}

	result := make(map[string]string)
	for key := range keys {
		if val, ok := deviceLabels[key]; ok {
			result[key] = val
		}
	}

	return result
}

func mergeStringMaps(base map[string]string, extras map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extras {
		result[k] = v
	}
	return result
}
