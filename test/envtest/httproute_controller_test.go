//go:build envtest

package envtest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kong/kubernetes-ingress-controller/v3/internal/controllers/gateway"
	"github.com/kong/kubernetes-ingress-controller/v3/internal/gatewayapi"
	"github.com/kong/kubernetes-ingress-controller/v3/internal/util/builder"
	"github.com/kong/kubernetes-ingress-controller/v3/test/helpers"
	"github.com/kong/kubernetes-ingress-controller/v3/test/mocks"
)

func TestHTTPRouteReconcilerProperlyReactsToReferenceGrant(t *testing.T) {
	t.Parallel()

	const (
		waitDuration = 5 * time.Second
		tickDuration = 100 * time.Millisecond
	)

	scheme := Scheme(t, WithGatewayAPI)
	cfg := Setup(t, scheme)
	client := NewControllerClient(t, scheme, cfg)

	reconciler := &gateway.HTTPRouteReconciler{
		Client:          client,
		DataplaneClient: mocks.Dataplane{},
	}

	// We use a deferred cancel to stop the manager and not wait for its timeout.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns := CreateNamespace(ctx, t, client)
	nsRoute := CreateNamespace(ctx, t, client)

	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      "backend-1",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Protocol:   corev1.ProtocolTCP,
					Port:       80,
					TargetPort: intstr.FromInt(80),
				},
			},
		},
	}
	require.NoError(t, client.Create(ctx, &svc))
	StartReconcilers(ctx, t, client.Scheme(), cfg, reconciler)

	gwc := gatewayapi.GatewayClass{
		Spec: gatewayapi.GatewayClassSpec{
			ControllerName: gateway.GetControllerName(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: uuid.NewString(),
			Annotations: map[string]string{
				"konghq.com/gatewayclass-unmanaged": "placeholder",
			},
		},
	}
	require.NoError(t, client.Create(ctx, &gwc))
	t.Cleanup(func() { _ = client.Delete(ctx, &gwc) })

	gw := gatewayapi.Gateway{
		Spec: gatewayapi.GatewaySpec{
			GatewayClassName: gatewayapi.ObjectName(gwc.Name),
			Listeners: []gatewayapi.Listener{
				{
					Name:     gatewayapi.SectionName("http"),
					Port:     gatewayapi.PortNumber(80),
					Protocol: gatewayapi.HTTPProtocolType,
					AllowedRoutes: &gatewayapi.AllowedRoutes{
						Namespaces: &gatewayapi.RouteNamespaces{
							From: lo.ToPtr(gatewayapi.NamespacesFromAll),
						},
					},
				},
			},
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      uuid.NewString(),
		},
	}
	require.NoError(t, client.Create(ctx, &gw))

	gwOld := gw.DeepCopy()
	gw.Status = gatewayapi.GatewayStatus{
		Addresses: []gatewayapi.GatewayStatusAddress{
			{
				Type:  lo.ToPtr(gatewayapi.IPAddressType),
				Value: "10.0.0.1",
			},
		},
		Conditions: []metav1.Condition{
			{
				Type:               "Programmed",
				Status:             metav1.ConditionTrue,
				Reason:             "Programmed",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: gw.Generation,
			},
			{
				Type:               "Accepted",
				Status:             metav1.ConditionTrue,
				Reason:             "Accepted",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: gw.Generation,
			},
		},
		Listeners: []gatewayapi.ListenerStatus{
			{
				Name: "http",
				Conditions: []metav1.Condition{
					{
						Type:               "Accepted",
						Status:             metav1.ConditionTrue,
						Reason:             "Accepted",
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               "Programmed",
						Status:             metav1.ConditionTrue,
						Reason:             "Programmed",
						LastTransitionTime: metav1.Now(),
					},
				},
				SupportedKinds: []gatewayapi.RouteGroupKind{
					{
						Group: lo.ToPtr(gatewayapi.Group(gatewayv1.GroupVersion.Group)),
						Kind:  "HTTPRoute",
					},
				},
			},
		},
	}
	require.NoError(t, client.Status().Patch(ctx, &gw, ctrlclient.MergeFrom(gwOld)))

	route := gatewayapi.HTTPRoute{
		TypeMeta: metav1.TypeMeta{
			Kind:       "HTTPRoute",
			APIVersion: "v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: nsRoute.Name,
			Name:      uuid.NewString(),
		},
		Spec: gatewayapi.HTTPRouteSpec{
			CommonRouteSpec: gatewayapi.CommonRouteSpec{
				ParentRefs: []gatewayapi.ParentReference{{
					Name:      gatewayapi.ObjectName(gw.Name),
					Namespace: lo.ToPtr(gatewayapi.Namespace(ns.Name)),
				}},
			},
			Rules: []gatewayapi.HTTPRouteRule{{
				BackendRefs: builder.NewHTTPBackendRef("backend-1").WithNamespace(ns.Name).ToSlice(),
			}},
		},
	}
	require.NoError(t, client.Create(ctx, &route))

	nn := k8stypes.NamespacedName{
		Namespace: route.GetNamespace(),
		Name:      route.GetName(),
	}

	t.Logf("verifying that HTTPRoute has ResolvedRefs set to Status False and Reason RefNotPermitted")
	if !assert.Eventually(t,
		helpers.HTTPRouteEventuallyContainsConditions(ctx, t, client, nn,
			metav1.Condition{
				Type:   "ResolvedRefs",
				Status: "False",
				Reason: "RefNotPermitted",
			},
		),
		waitDuration, tickDuration,
	) {
		t.Fatal(printHTTPRoutesConditions(ctx, client, nn))
	}

	rg := gatewayapi.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      uuid.NewString(),
		},
		Spec: gatewayapi.ReferenceGrantSpec{
			From: []gatewayapi.ReferenceGrantFrom{
				{
					Group:     gatewayapi.Group(gatewayv1.GroupVersion.Group),
					Kind:      "HTTPRoute",
					Namespace: gatewayapi.Namespace(nsRoute.Name),
				},
			},
			To: []gatewayapi.ReferenceGrantTo{
				{
					Group: "",
					Kind:  "Service",
				},
			},
		},
	}
	require.NoError(t, client.Create(ctx, &rg))
	t.Logf("verifying that HTTPRoute gets accepted by HTTPRouteReconciler after relevant ReferenceGrant gets created")
	if !assert.Eventually(t,
		helpers.HTTPRouteEventuallyContainsConditions(ctx, t, client, nn,
			metav1.Condition{
				Type:   "ResolvedRefs",
				Status: "True",
				Reason: "ResolvedRefs",
			},
			metav1.Condition{
				Type:   "Accepted",
				Status: "True",
				Reason: "Accepted",
			},
			// Programmed condition requires a bit more work with mocks.
			// It's set only when KubernetesObjectReports are enabled in the underlying
			// dataplane client and then it relies on what's returned by
			// dataplane client in KubernetesObjectConfigurationStatus().
			// This can be done but it's not the main focus of this test.
			// Related: https://github.com/Kong/kubernetes-ingress-controller/issues/3793
		),
		waitDuration, tickDuration,
	) {
		t.Fatal(printHTTPRoutesConditions(ctx, client, nn))
	}

	require.NoError(t, client.Delete(ctx, &rg))
	t.Logf("verifying that HTTPRoute gets its ResolvedRefs condition to Status False and Reason RefNotPermitted when relevant ReferenceGrant gets deleted")

	if !assert.Eventually(t,
		helpers.HTTPRouteEventuallyContainsConditions(ctx, t, client, nn,
			metav1.Condition{
				Type:   "ResolvedRefs",
				Status: "False",
				Reason: "RefNotPermitted",
			},
		),
		waitDuration, tickDuration,
	) {
		t.Fatal(printHTTPRoutesConditions(ctx, client, nn))
	}
}

func printHTTPRoutesConditions(ctx context.Context, client ctrlclient.Client, nn k8stypes.NamespacedName) string {
	var route gatewayapi.HTTPRoute
	err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: nn.Namespace, Name: nn.Name}, &route)
	if err != nil {
		return fmt.Sprintf("Failed to get HTTPRoute %s/%s when trying to print its conditions", nn.Namespace, nn.Name)
	}

	if len(route.Status.Parents) == 0 {
		return fmt.Sprintf("HTTPRoute %s/%s has no parents in Status", nn.Namespace, nn.Name)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HTTPRoute %s/%s has the following Parents in Status:", nn.Namespace, nn.Name))
	for _, p := range route.Status.Parents {
		if p.ParentRef.Namespace != nil {
			_, _ = sb.WriteString(fmt.Sprintf("\nParent %s/%s: ", *p.ParentRef.Namespace, string(p.ParentRef.Name)))
		} else {
			_, _ = sb.WriteString(fmt.Sprintf("\nParent %s: ", string(p.ParentRef.Name)))
		}
		for _, c := range p.Conditions {
			s := fmt.Sprintf(
				"\n\tcondition: Type:%s, Status:%s, Reason:%s, ObservedGeneration:%d",
				c.Type, c.Status, c.Reason, c.ObservedGeneration,
			)
			_, _ = sb.WriteString(s)
		}
		_ = sb.WriteByte('\n')
	}
	return sb.String()
}
