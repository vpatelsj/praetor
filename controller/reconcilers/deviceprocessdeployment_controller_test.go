package reconcilers

import (
	"context"
	"strings"
	"testing"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcileCreatesDeviceProcesses(t *testing.T) {
	scheme := testScheme(t)
	deployment := sampleDeployment("dpd", map[string]string{"role": "leaf"})
	switchA := networkSwitch("leaf-a", map[string]string{"role": "leaf", "rack": "r1"})
	switchB := networkSwitch("leaf-b", map[string]string{"role": "leaf", "rack": "r2"})

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployment, switchA, switchB).
		Build()

	reconciler := &DeviceProcessDeploymentReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	ctx := context.Background()
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	var processes apiv1alpha1.DeviceProcessList
	if err := k8sClient.List(ctx, &processes, client.InNamespace(deployment.Namespace)); err != nil {
		t.Fatalf("list deviceprocesses: %v", err)
	}

	if len(processes.Items) != 2 {
		t.Fatalf("expected 2 DeviceProcesses, got %d", len(processes.Items))
	}

	for i := range processes.Items {
		proc := &processes.Items[i]
		if !metav1.IsControlledBy(proc, deployment) {
			t.Fatalf("process %s missing owner reference", proc.Name)
		}
		if proc.Spec.DeviceRef.Kind != apiv1alpha1.DeviceRefKindNetworkSwitch {
			t.Fatalf("unexpected device kind %s", proc.Spec.DeviceRef.Kind)
		}
	}
}

func TestReconcileDeletesStaleDeviceProcesses(t *testing.T) {
	scheme := testScheme(t)
	deployment := sampleDeployment("dpd", map[string]string{"role": "leaf"})
	switchA := networkSwitch("leaf-a", map[string]string{"role": "leaf", "rack": "r1"})
	switchB := networkSwitch("leaf-b", map[string]string{"role": "leaf", "rack": "r2"})

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployment, switchA, switchB).
		Build()

	reconciler := &DeviceProcessDeploymentReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	ctx := context.Background()
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("initial reconcile returned error: %v", err)
	}

	var fetched apiv1alpha1.DeviceProcessDeployment
	if err := k8sClient.Get(ctx, request.NamespacedName, &fetched); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	fetched.Spec.Selector.MatchLabels = map[string]string{"role": "leaf", "rack": "r1"}
	if err := k8sClient.Update(ctx, &fetched); err != nil {
		t.Fatalf("update deployment selector: %v", err)
	}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("second reconcile returned error: %v", err)
	}

	var processes apiv1alpha1.DeviceProcessList
	if err := k8sClient.List(ctx, &processes, client.InNamespace(deployment.Namespace)); err != nil {
		t.Fatalf("list deviceprocesses: %v", err)
	}

	if len(processes.Items) != 1 {
		t.Fatalf("expected 1 DeviceProcess after selector change, got %d", len(processes.Items))
	}

	if processes.Items[0].Spec.DeviceRef.Name != "leaf-a" {
		t.Fatalf("expected surviving process for leaf-a, got %s", processes.Items[0].Spec.DeviceRef.Name)
	}
}

func TestDeviceProcessNameHashIncludesDeployment(t *testing.T) {
	deviceName := strings.Repeat("a", 240)

	nameA := deviceProcessName("deployment-a", deviceName)
	nameB := deviceProcessName("deployment-b", deviceName)

	if nameA == nameB {
		t.Fatalf("expected hashed names to differ across deployments, got %s", nameA)
	}
	if len(nameA) > validation.DNS1123SubdomainMaxLength || len(nameB) > validation.DNS1123SubdomainMaxLength {
		t.Fatalf("hashed names exceed DNS subdomain length")
	}
}

func TestBuildDesiredDeviceProcessSkipsInvalidDeviceLabels(t *testing.T) {
	scheme := testScheme(t)
	deployment := sampleDeployment("dpd", map[string]string{"role": "leaf"})
	badValue := strings.Repeat("r", 70)
	device := networkSwitch("leaf-a", map[string]string{"rack": badValue})

	proc := buildDesiredDeviceProcess(context.Background(), deployment, device, "dpd-leaf-a")
	if _, ok := proc.Labels["rack"]; ok {
		t.Fatalf("expected invalid device label to be skipped")
	}
	if proc.Labels[deviceProcessDeploymentUIDKey] != string(deployment.UID) {
		t.Fatalf("missing deployment UID label on DeviceProcess")
	}
	_ = scheme // silence unused warning for consistency with other tests when scheme is extended
}

func TestUpsertWithoutSSAUpdatesExistingWithoutDroppingMetadata(t *testing.T) {
	scheme := testScheme(t)
	existing := &apiv1alpha1.DeviceProcess{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "dpd-device",
			Namespace:   "default",
			Labels:      map[string]string{"agent": "keep"},
			Annotations: map[string]string{"agent": "keep"},
		},
		Spec: apiv1alpha1.DeviceProcessSpec{},
	}

	desired := existing.DeepCopy()
	desired.Labels = map[string]string{
		"controller": "set",
	}
	desired.Annotations = map[string]string{"controller": "set"}

	r := &DeviceProcessDeploymentReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	if _, err := r.upsertWithoutSSA(context.Background(), desired, false); err != nil {
		t.Fatalf("upsertWithoutSSA returned error: %v", err)
	}

	var updated apiv1alpha1.DeviceProcess
	if err := r.Get(context.Background(), types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &updated); err != nil {
		t.Fatalf("get updated process: %v", err)
	}

	if updated.Labels["agent"] != "keep" {
		t.Fatalf("agent label was dropped during update")
	}
	if updated.Annotations["agent"] != "keep" {
		t.Fatalf("agent annotation was dropped during update")
	}
	if updated.Labels["controller"] != "set" || updated.Annotations["controller"] != "set" {
		t.Fatalf("controller metadata not applied")
	}
}

func TestRequestsForNetworkSwitchMatchesSelectors(t *testing.T) {
	scheme := testScheme(t)
	deployment := sampleDeployment("dpd", map[string]string{"role": "leaf", "rack": "r1"})
	switchObj := networkSwitch("leaf-a", map[string]string{"role": "leaf", "rack": "r1"})

	indexFn := func(obj client.Object) []string {
		dep, ok := obj.(*apiv1alpha1.DeviceProcessDeployment)
		if !ok {
			return nil
		}
		keys := selectorLabelKeys(&dep.Spec.Selector)
		res := make([]string, 0, len(keys))
		for k := range keys {
			res = append(res, k)
		}
		return res
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployment).
		WithIndex(&apiv1alpha1.DeviceProcessDeployment{}, selectorKeysIndex, indexFn).
		Build()

	reconciler := &DeviceProcessDeploymentReconciler{
		Client:   cl,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	reqs := reconciler.requestsForNetworkSwitch(context.Background(), switchObj, nil)

	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}}
	if reqs[0] != expected {
		t.Fatalf("unexpected request: %+v", reqs[0])
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add api scheme: %v", err)
	}

	networkGVK := schema.GroupVersionKind{Group: "azure.com", Version: "v1alpha1", Kind: "NetworkSwitch"}
	scheme.AddKnownTypeWithName(networkGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(networkGVK.GroupVersion().WithKind("NetworkSwitchList"), &unstructured.UnstructuredList{})

	return scheme
}

func sampleDeployment(name string, selector map[string]string) *apiv1alpha1.DeviceProcessDeployment {
	return &apiv1alpha1.DeviceProcessDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name + "-uid"),
		},
		Spec: apiv1alpha1.DeviceProcessDeploymentSpec{
			Selector: metav1.LabelSelector{MatchLabels: selector},
			Template: apiv1alpha1.DeviceProcessTemplate{
				Metadata: apiv1alpha1.DeviceProcessTemplateMetadata{
					Labels: map[string]string{"component": "process"},
				},
				Spec: apiv1alpha1.DeviceProcessTemplateSpec{
					Artifact:  apiv1alpha1.DeviceProcessArtifact{Type: apiv1alpha1.ArtifactTypeOCI, URL: "oci://example"},
					Execution: apiv1alpha1.DeviceProcessExecution{Backend: apiv1alpha1.DeviceProcessBackendSystemd, Command: []string{"/bin/echo"}},
				},
			},
		},
	}
}

func networkSwitch(name string, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "azure.com", Version: "v1alpha1", Kind: "NetworkSwitch"})
	obj.SetName(name)
	obj.SetNamespace("default")
	obj.SetLabels(labels)
	return obj
}
