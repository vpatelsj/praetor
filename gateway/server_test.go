package gateway

import (
	"context"
	"testing"
	"time"

	apiv1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type nopRecorder struct{}

func (nopRecorder) Event(_ runtime.Object, _, _, _ string)            {}
func (nopRecorder) Eventf(_ runtime.Object, _, _, _ string, _ ...any) {}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apiv1alpha1 scheme: %v", err)
	}
	return scheme
}

func boolPtr(v bool) *bool       { return &v }
func int64Ptr(v int64) *int64    { return &v }
func stringPtr(v string) *string { return &v }

func TestObservationClearsStalePIDAndStartTime(t *testing.T) {
	ctx := context.Background()
	scheme := testScheme(t)

	started := metav1.NewTime(time.Now().UTC().Add(-1 * time.Minute))
	proc := &apiv1alpha1.DeviceProcess{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
		Spec: apiv1alpha1.DeviceProcessSpec{
			DeviceRef: apiv1alpha1.DeviceRef{Kind: apiv1alpha1.DeviceRefKindServer, Name: "dev"},
			Execution: apiv1alpha1.DeviceProcessExecution{Backend: apiv1alpha1.DeviceProcessBackendSystemd, Command: []string{"/bin/true"}},
			Artifact:  apiv1alpha1.DeviceProcessArtifact{Type: apiv1alpha1.ArtifactTypeFile, URL: "/bin/true"},
		},
		Status: apiv1alpha1.DeviceProcessStatus{PID: 123, StartTime: &started},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(proc).WithStatusSubresource(&apiv1alpha1.DeviceProcess{}).Build()
	g := &Gateway{client: c, recorder: nopRecorder{}}

	obs := Observation{
		Namespace:        "ns",
		Name:             "p",
		ObservedSpecHash: "hash",
		ProcessStarted:   boolPtr(false),
		Healthy:          boolPtr(false),
		PID:              int64Ptr(0),
		StartTime:        stringPtr(""),
	}

	if err := g.updateStatusForObservation(ctx, "dev", obs, nil); err != nil {
		t.Fatalf("updateStatusForObservation: %v", err)
	}

	var got apiv1alpha1.DeviceProcess
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "p"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.PID != 0 {
		t.Fatalf("expected pid=0, got %d", got.Status.PID)
	}
	if got.Status.StartTime != nil {
		t.Fatalf("expected startTime cleared, got %v", got.Status.StartTime)
	}
}
