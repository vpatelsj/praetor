package reconcilers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"reflect"
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
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	fieldManagerName              = "deviceprocess-controller"
	deviceProcessDeploymentKey    = "deviceprocessdeployment"
	deviceProcessDeploymentUIDKey = "deviceprocessdeployment-uid"
	selectorKeysIndex             = "selectorKeys"
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
	ctx := context.Background()
	if err := mgr.GetFieldIndexer().IndexField(ctx, &apiv1alpha1.DeviceProcessDeployment{}, selectorKeysIndex, func(obj client.Object) []string {
		dep, ok := obj.(*apiv1alpha1.DeviceProcessDeployment)
		if !ok {
			return nil
		}
		keys := selectorLabelKeys(&dep.Spec.Selector)
		result := make([]string, 0, len(keys))
		for k := range keys {
			result = append(result, k)
		}
		return result
	}); err != nil {
		return err
	}

	networkSwitch := &unstructured.Unstructured{}
	networkSwitch.SetGroupVersionKind(schema.GroupVersionKind{Group: "azure.com", Version: "v1alpha1", Kind: "NetworkSwitch"})

	networkSwitchPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return true },
		DeleteFunc: func(e event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObj := e.ObjectOld
			newObj := e.ObjectNew
			if oldObj == nil || newObj == nil {
				return true
			}
			labelsChanged := !reflect.DeepEqual(oldObj.GetLabels(), newObj.GetLabels())
			annotationsChanged := !reflect.DeepEqual(oldObj.GetAnnotations(), newObj.GetAnnotations())
			generationChanged := oldObj.GetGeneration() != newObj.GetGeneration()
			return labelsChanged || annotationsChanged || generationChanged
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.DeviceProcessDeployment{}).
		Watches(networkSwitch, handler.Funcs{
			CreateFunc: func(c context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
				for _, req := range r.requestsForNetworkSwitch(c, e.Object) {
					q.Add(req)
				}
			},
			UpdateFunc: func(c context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
				for _, req := range r.requestsForNetworkSwitch(c, e.ObjectOld) {
					q.Add(req)
				}
				for _, req := range r.requestsForNetworkSwitch(c, e.ObjectNew) {
					q.Add(req)
				}
			},
			DeleteFunc: func(c context.Context, e event.DeleteEvent, q workqueue.RateLimitingInterface) {
				for _, req := range r.requestsForNetworkSwitch(c, e.Object) {
					q.Add(req)
				}
			},
		}, builder.WithPredicates(networkSwitchPredicate)).
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
	ensuredCount := 0

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
		}
		ensuredCount++
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

	logger.Info("reconcile complete", "ensured", ensuredCount, "created", createdCount, "deleted", deletedCount)

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

	desired := buildDesiredDeviceProcess(ctx, deployment, device, name)
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
		} else {
			return created, nil
		}
	}

	var current apiv1alpha1.DeviceProcess
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	if err := r.Get(ctx, key, &current); err != nil {
		return created, err
	}

	current.Labels = mergeStringMaps(current.Labels, desired.Labels)
	current.Annotations = mergeStringMaps(current.Annotations, desired.Annotations)
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
		if uidLabel, ok := process.Labels[deviceProcessDeploymentUIDKey]; ok && uidLabel != string(deployment.UID) {
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

func (r *DeviceProcessDeploymentReconciler) requestsForNetworkSwitch(ctx context.Context, obj client.Object) []reconcile.Request {
	switchObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}

	labelsMap := switchObj.GetLabels()
	if len(labelsMap) == 0 {
		return nil
	}

	labelSet := labels.Set(labelsMap)
	seen := make(map[types.NamespacedName]struct{})
	requests := make([]reconcile.Request, 0)

	for key := range labelsMap {
		var deployments apiv1alpha1.DeviceProcessDeploymentList
		if err := r.List(ctx, &deployments,
			client.InNamespace(switchObj.GetNamespace()),
			client.MatchingFields{selectorKeysIndex: key},
		); err != nil {
			log.FromContext(ctx).Error(err, "list deployments for switch", "key", key)
			continue
		}

		for i := range deployments.Items {
			dep := deployments.Items[i]
			selector, err := metav1.LabelSelectorAsSelector(&dep.Spec.Selector)
			if err != nil {
				continue
			}
			if !selector.Matches(labelSet) {
				continue
			}
			nn := types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}
			if _, exists := seen[nn]; exists {
				continue
			}
			seen[nn] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: nn})
		}
	}

	return requests
}

func (r *DeviceProcessDeploymentReconciler) listNetworkSwitches(ctx context.Context, namespace string, selector labels.Selector) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	gvk := schema.GroupVersion{Group: "azure.com", Version: "v1alpha1"}.WithKind("NetworkSwitchList")
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

func buildDesiredDeviceProcess(ctx context.Context, deployment *apiv1alpha1.DeviceProcessDeployment, device *unstructured.Unstructured, name string) *apiv1alpha1.DeviceProcess {
	template := deployment.Spec.Template
	selector := deployment.Spec.Selector

	labels := mergeStringMaps(template.Metadata.Labels, map[string]string{
		"app":                         deployment.Name,
		deviceProcessDeploymentKey:    deployment.Name,
		deviceProcessDeploymentUIDKey: string(deployment.UID),
	})
	labels = mergeStringMaps(labels, selectedDeviceLabels(ctx, device.GetLabels(), &selector))

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

	hash := sha1.Sum([]byte(fmt.Sprintf("%s:%s", deploymentName, deviceName)))
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

func selectedDeviceLabels(ctx context.Context, deviceLabels map[string]string, selector *metav1.LabelSelector) map[string]string {
	if len(deviceLabels) == 0 {
		return nil
	}

	logger := log.FromContext(ctx)
	keys := selectorLabelKeys(selector)
	for _, k := range []string{"role", "type", "rack"} {
		keys.Insert(k)
	}

	result := make(map[string]string)
	for key := range keys {
		val, ok := deviceLabels[key]
		if !ok {
			continue
		}

		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			logger.V(1).Info("skipping device label with invalid key", "key", key, "errors", strings.Join(errs, "; "))
			continue
		}
		if errs := validation.IsValidLabelValue(val); len(errs) > 0 {
			logger.V(1).Info("skipping device label with invalid value", "key", key, "errors", strings.Join(errs, "; "))
			continue
		}

		result[key] = val
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
