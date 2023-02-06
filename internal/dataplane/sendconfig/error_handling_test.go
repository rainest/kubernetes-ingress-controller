package sendconfig

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	deckutils "github.com/kong/deck/utils"
	"github.com/kong/go-kong/kong"
	"github.com/kong/kubernetes-ingress-controller/v2/internal/metrics"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestParseFlatEntityErrors(t *testing.T) {
	log := logrus.New()
	tests := []struct {
		name    string
		body    []byte
		want    []ResourceError
		wantErr bool
	}{
		{
			name: "a route nested under a service, with two and one errors, respectively",
			want: []ResourceError{
				{
					Name:       "scallion",
					Namespace:  "default",
					Kind:       "Ingress",
					APIVersion: "networking.k8s.io/v1",
					UID:        "d7300db1-14eb-5a09-b594-2db904ed8eca",
					Problems: map[string]string{
						"methods": "cannot set methods when protocols is grpc or grpcs",
					},
				},
				{
					Name:       "turnip",
					Namespace:  "default",
					Kind:       "Ingress",
					APIVersion: "networking.k8s.io/v1",
					UID:        "d7300db1-14eb-5a09-b594-2db904ed8eca",
					Problems: map[string]string{
						"methods[0]": "expected a string",
						"methods[1]": "expected a string",
					},
				},
				{
					Name:       "radish",
					Namespace:  "default",
					Kind:       "Service",
					UID:        "b8aa692c-6d8d-580e-a767-a7dbc1f58344",
					APIVersion: "v1",
					Problems: map[string]string{
						"read_timeout":  "expected an integer",
						"write_timeout": "expected an integer",
					},
				},
			},
			wantErr: false,
			body: []byte(`{
    "code": 14,
    "fields": {
        "routes": [
            null,
            {
                "methods": "cannot set 'methods' when 'protocols' is 'grpc' or 'grpcs'"
            },
            {
                "strip_path": "cannot set 'strip_path' when 'protocols' is 'grpc' or 'grpcs'"
            }
        ],
        "services": [
            {
                "read_timeout": "expected an integer"
            }
        ]
    },
    "flattened_errors": [
        {
            "entity": {
                "ca_certificates": null,
                "connect_timeout": 60000,
                "created_at": 1663285589,
                "enabled": true,
                "host": "echo.default.80.svc",
                "name": "default.echo.pnum-80",
                "path": "/",
                "port": 80,
                "protocol": "http",
                "read_timeout": true,
                "retries": 5,
                "tags": null,
                "tls_verify": null,
                "tls_verify_depth": null,
                "updated_at": 1663285589,
                "write_timeout": 60000
            },
            "entity_name": "default.echo.pnum-80",
            "entity_type": "service",
            "errors": [
                {
                    "field": "read_timeout",
                    "message": "expected an integer",
                    "type": "field"
                }
            ]
        },
        {
            "entity": {
                "created_at": 1663285589,
                "destinations": null,
                "headers": null,
                "hosts": null,
                "https_redirect_status_code": 426,
                "methods": [
                    "GET"
                ],
                "name": "default.demo.00",
                "path_handling": "v0",
                "paths": [
                    "/foo"
                ],
                "preserve_host": true,
                "protocols": [
                    "grpc"
                ],
                "regex_priority": 100,
                "request_buffering": true,
                "response_buffering": true,
                "snis": null,
                "sources": null,
                "strip_path": false,
                "tags": null,
                "updated_at": 1663285589
            },
            "entity_name": "default.demo.00",
            "entity_type": "route",
            "errors": [
                {
                    "field": "methods",
                    "message": "cannot set 'methods' when 'protocols' is 'grpc' or 'grpcs'",
                    "type": "field"
                }
            ]
        },
        {
            "entity": {
                "created_at": 1663285589,
                "destinations": null,
                "headers": null,
                "hosts": null,
                "https_redirect_status_code": 426,
                "methods": null,
                "name": "default.demo.01",
                "path_handling": "v0",
                "paths": [
                    "/foo"
                ],
                "preserve_host": true,
                "protocols": [
                    "grpc"
                ],
                "regex_priority": 100,
                "request_buffering": true,
                "response_buffering": true,
                "snis": null,
                "sources": null,
                "strip_path": true,
                "tags": null,
                "updated_at": 1663285589
            },
            "entity_name": "default.demo.01",
            "entity_type": "route",
            "errors": [
                {
                    "field": "strip_path",
                    "message": "cannot set 'strip_path' when 'protocols' is 'grpc' or 'grpcs'",
                    "type": "field"
                }
            ]
        }
    ],
    "message": "declarative config is invalid: {routes={[2]={methods=\"cannot set 'methods' when 'protocols' is 'grpc' or 'grpcs'\"},[3]={strip_path=\"cannot set 'strip_path' when 'protocols' is 'grpc' or 'grpcs'\"}},services={{read_timeout=\"expected an integer\"}}}",
    "name": "invalid declarative configuration"
}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFlatEntityErrors(tt.body, log)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseFlatEntityErrors() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			require.Equal(t, got, tt.want)
		})
	}
}

func TestPushFailureReason(t *testing.T) {
	apiConflictErr := kong.NewAPIErrorWithRaw(http.StatusConflict, "conflict api error", []byte{})
	networkErr := net.UnknownNetworkError("network error")
	genericError := errors.New("generic error")

	testCases := []struct {
		name           string
		err            error
		expectedReason string
	}{
		{
			name:           "generic_error",
			err:            genericError,
			expectedReason: metrics.FailureReasonOther,
		},
		{
			name:           "api_conflict_error",
			err:            apiConflictErr,
			expectedReason: metrics.FailureReasonConflict,
		},
		{
			name:           "api_conflict_error_wrapped",
			err:            fmt.Errorf("wrapped conflict api err: %w", apiConflictErr),
			expectedReason: metrics.FailureReasonConflict,
		},
		{
			name:           "deck_config_conflict_error_empty",
			err:            deckConfigConflictError{},
			expectedReason: metrics.FailureReasonConflict,
		},
		{
			name:           "deck_config_conflict_error_with_generic_error",
			err:            deckConfigConflictError{genericError},
			expectedReason: metrics.FailureReasonConflict,
		},
		{
			name:           "deck_err_array_with_api_conflict_error",
			err:            deckutils.ErrArray{Errors: []error{apiConflictErr}},
			expectedReason: metrics.FailureReasonConflict,
		},
		{
			name:           "wrapped_deck_err_array_with_api_conflict_error",
			err:            fmt.Errorf("wrapped: %w", deckutils.ErrArray{Errors: []error{apiConflictErr}}),
			expectedReason: metrics.FailureReasonConflict,
		},
		{
			name:           "deck_err_array_with_generic_error",
			err:            deckutils.ErrArray{Errors: []error{genericError}},
			expectedReason: metrics.FailureReasonOther,
		},
		{
			name:           "deck_err_array_empty",
			err:            deckutils.ErrArray{Errors: []error{genericError}},
			expectedReason: metrics.FailureReasonOther,
		},
		{
			name:           "network_error",
			err:            networkErr,
			expectedReason: metrics.FailureReasonNetwork,
		},
		{
			name:           "network_error_wrapped_in_deck_config_conflict_error",
			err:            deckConfigConflictError{networkErr},
			expectedReason: metrics.FailureReasonNetwork,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reason := pushFailureReason(tc.err)
			require.Equal(t, tc.expectedReason, reason)
		})
	}
}
