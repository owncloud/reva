// Copyright 2018-2021 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package micro

import (
	"context"
	"testing"
	"time"

	registrypb "github.com/cs3org/go-cs3apis/cs3/app/registry/v1beta1"
	"github.com/google/uuid"
	oreg "github.com/owncloud/ocis/v2/ocis-pkg/registry"
	mreg "go-micro.dev/v4/registry"
)

func init() {
	oreg.Configure("memory")
	_ = oreg.GetRegistry()
}

func TestFindProviders(t *testing.T) {

	testCases := []struct {
		name              string
		mimeTypes         []*mimeTypeConfig
		regProviders      []*registrypb.ProviderInfo
		mimeType          string
		expectedRes       []*registrypb.ProviderInfo
		expectedErr       error
		registryNamespace string
	}{
		{
			name:              "no mime types registered",
			registryNamespace: "noMimeTypesRegistered",
			mimeTypes:         []*mimeTypeConfig{},
			mimeType:          "SOMETHING",
			expectedErr:       mreg.ErrNotFound,
		},
		{
			name:              "one provider registered for one mime type",
			registryNamespace: "oneProviderRegistererdForOneMimeType",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "some Address",
				},
			},
			regProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "some Name",
				},
			},
			mimeType:    "text/json",
			expectedErr: nil,
			expectedRes: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "some Name",
				},
			},
		},
		{
			name:              "more providers registered for one mime type",
			registryNamespace: "moreProvidersRegisteredForOneMimeType",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "provider2",
				},
			},
			regProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "provider1",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "provider2",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.3:65535",
					Name:      "provider3",
				},
			},
			mimeType:    "text/json",
			expectedErr: nil,
			expectedRes: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "provider1",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "provider2",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.3:65535",
					Name:      "provider3",
				},
			},
		},
		{
			name:              "more providers registered for different mime types",
			registryNamespace: "moreProvidersRegisteredForDifferentMimeTypes",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "provider2",
				},
				{
					MimeType:   "text/xml",
					Extension:  "xml",
					Name:       "XML File",
					Icon:       "https://example.org/icons&file=xml.png",
					DefaultApp: "provider1",
				},
			},
			regProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json", "text/xml"},
					Address:   "127.0.0.1:65535",
					Name:      "provider1",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "provider2",
				},
				{
					MimeTypes: []string{"text/xml"},
					Address:   "127.0.0.3:65535",
					Name:      "provider3",
				},
			},
			mimeType:    "text/json",
			expectedErr: nil,
			expectedRes: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json", "text/xml"},
					Address:   "127.0.0.1:65535",
					Name:      "provider1",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "provider2",
				},
			},
		},
	}

	for _, tt := range testCases {

		ctx := context.TODO()

		// register all the providers
		for _, p := range tt.regProviders {
			err := registerWithMicroReg(tt.registryNamespace, p)
			if err != nil {
				t.Error("unexpected error adding a new provider in the registry:", err)
			}
		}

		registry, err := New(map[string]interface{}{
			"mime_types": tt.mimeTypes,
			"namespace":  tt.registryNamespace, // TODO: move this to a const
		})

		if err != nil {
			t.Error("unexpected error creating the registry:", err)
		}

		providers, err := registry.FindProviders(ctx, tt.mimeType)

		// check that the error returned by FindProviders is the same as the expected
		if tt.expectedErr != err {
			t.Errorf("different error returned: got=%v expected=%v", err, tt.expectedErr)
		}

		// expected: slice of pointers, got: slice of pointers with weird opague notation
		if !providersEquals(providers, tt.expectedRes) {
			t.Errorf("providers list different from expected: \n\tgot=%v\n\texp=%v", providers, tt.expectedRes)
		}

	}

}

// func TestFindProvidersWithPriority(t *testing.T) {
//
//	testCases := []struct {
//		name              string
//		mimeTypes         []*mimeTypeConfig
//		regProviders      []*registrypb.ProviderInfo
//		mimeType          string
//		expectedRes       []*registrypb.ProviderInfo
//		expectedErr       error
//		registryNamespace string
//	}{
//		{
//			name:              "no mime types registered",
//			registryNamespace: "noMimeTypesRegistered",
//			mimeTypes:         []*mimeTypeConfig{},
//			mimeType:          "SOMETHING",
//			expectedErr:       mreg.ErrNotFound,
//		},
//		{
//			name:              "one provider registered for one mime type",
//			registryNamespace: "oneProviderRegisteredForOneMimeType",
//			mimeTypes: []*mimeTypeConfig{
//				{
//					MimeType:   "text/json",
//					Extension:  "json",
//					Name:       "JSON File",
//					Icon:       "https://example.org/icons&file=json.png",
//					DefaultApp: "some Address",
//				},
//			},
//			regProviders: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.1:65535",
//					Name:      "some Name",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("100"),
//							},
//						},
//					},
//				},
//			},
//			mimeType:    "text/json",
//			expectedErr: nil,
//			expectedRes: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.1:65535",
//					Name:      "some Name",
//				},
//			},
//		},
//		{
//			name:              "more providers registered for one mime type",
//			registryNamespace: "moreProvidersRegisteredForOneMimeType",
//			mimeTypes: []*mimeTypeConfig{
//				{
//					MimeType:   "text/json",
//					Extension:  "json",
//					Name:       "JSON File",
//					Icon:       "https://example.org/icons&file=json.png",
//					DefaultApp: "provider2",
//				},
//			},
//			regProviders: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.1:65535",
//					Name:      "provider1",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("10"),
//							},
//						},
//					},
//				},
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.2:65535",
//					Name:      "provider2",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("20"),
//							},
//						},
//					},
//				},
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.3:65535",
//					Name:      "provider3",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("5"),
//							},
//						},
//					},
//				},
//			},
//			mimeType:    "text/json",
//			expectedErr: nil,
//			expectedRes: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.2:65535",
//					Name:      "provider2",
//				},
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.1:65535",
//					Name:      "provider1",
//				},
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.3:65535",
//					Name:      "provider3",
//				},
//			},
//		},
//		{
//			name:              "more providers registered for different mime types",
//			registryNamespace: "moreProvidersRegisteredForDifferentMimeTypes",
//			mimeTypes: []*mimeTypeConfig{
//				{
//					MimeType:   "text/json",
//					Extension:  "json",
//					Name:       "JSON File",
//					Icon:       "https://example.org/icons&file=json.png",
//					DefaultApp: "provider2",
//				},
//				{
//					MimeType:   "text/xml",
//					Extension:  "xml",
//					Name:       "XML File",
//					Icon:       "https://example.org/icons&file=xml.png",
//					DefaultApp: "provider1",
//				},
//			},
//			regProviders: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/json", "text/xml"},
//					Address:   "127.0.0.1:65535",
//					Name:      "provider1",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("5"),
//							},
//						},
//					},
//				},
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.2:65535",
//					Name:      "provider2",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("100"),
//							},
//						},
//					},
//				},
//				{
//					MimeTypes: []string{"text/xml"},
//					Address:   "127.0.0.3:65535",
//					Name:      "provider3",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("20"),
//							},
//						},
//					},
//				},
//			},
//			mimeType:    "text/json",
//			expectedErr: nil,
//			expectedRes: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.2:65535",
//					Name:      "provider2",
//				},
//				{
//					MimeTypes: []string{"text/json", "text/xml"},
//					Address:   "127.0.0.1:65535",
//					Name:      "provider1",
//				},
//			},
//		},
//		{
//			name:              "more providers registered for different mime types2",
//			registryNamespace: "moreProvidersRegisteredForDifferentMimeTypes2",
//			mimeTypes: []*mimeTypeConfig{
//				{
//					MimeType:   "text/json",
//					Extension:  "json",
//					Name:       "JSON File",
//					Icon:       "https://example.org/icons&file=json.png",
//					DefaultApp: "provider2",
//				},
//				{
//					MimeType:   "text/xml",
//					Extension:  "xml",
//					Name:       "XML File",
//					Icon:       "https://example.org/icons&file=xml.png",
//					DefaultApp: "provider1",
//				},
//			},
//			regProviders: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/json", "text/xml"},
//					Address:   "127.0.0.1:65535",
//					Name:      "provider1",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("5"),
//							},
//						},
//					},
//				},
//				{
//					MimeTypes: []string{"text/json"},
//					Address:   "127.0.0.2:65535",
//					Name:      "provider2",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("100"),
//							},
//						},
//					},
//				},
//				{
//					MimeTypes: []string{"text/xml"},
//					Address:   "127.0.0.3:65535",
//					Name:      "provider3",
//					Opaque: &typesv1beta1.Opaque{
//						Map: map[string]*typesv1beta1.OpaqueEntry{
//							"priority": {
//								Decoder: "plain",
//								Value:   []byte("20"),
//							},
//						},
//					},
//				},
//			},
//			mimeType:    "text/xml",
//			expectedErr: nil,
//			expectedRes: []*registrypb.ProviderInfo{
//				{
//					MimeTypes: []string{"text/xml"},
//					Address:   "127.0.0.3:65535",
//					Name:      "provider3",
//				},
//				{
//					MimeTypes: []string{"text/json", "text/xml"},
//					Address:   "127.0.0.1:65535",
//					Name:      "provider1",
//				},
//			},
//		},
//	}
//
//	for _, tt := range testCases {
//
//		t.Run(tt.name, func(t *testing.T) {
//
//			ctx := context.TODO()
//
//			// register all the providers
//			for _, p := range tt.regProviders {
//				err := registerWithMicroReg(tt.registryNamespace, p)
//				if err != nil {
//					t.Fatal("unexpected error adding a new provider in the registry:", err)
//				}
//			}
//
//			registry, err := New(map[string]interface{}{
//				"mime_types": tt.mimeTypes,
//				"namespace":  tt.registryNamespace,
//			})
//
//			if err != nil {
//				t.Fatal("unexpected error creating the registry:", err)
//			}
//
//			providers, err := registry.FindProviders(ctx, tt.mimeType)
//
//			// check that the error returned by FindProviders is the same as the expected
//			if tt.expectedErr != err {
//				t.Fatalf("different error returned: got=%v expected=%v", err, tt.expectedErr)
//			}
//
//			if !providersEquals(providers, tt.expectedRes) {
//				t.Fatalf("providers list different from expected: \n\tgot=%v\n\texp=%v", providers, tt.expectedRes)
//			}
//
//		})
//
//	}
//
// }

func TestListSupportedMimeTypes(t *testing.T) {
	testCases := []struct {
		name              string
		registryNamespace string
		mimeTypes         []*mimeTypeConfig
		newProviders      []*registrypb.ProviderInfo
		expected          []*registrypb.MimeTypeInfo
	}{
		{
			name:              "one mime type - no provider registered",
			registryNamespace: "oneMimeTypeNoProviderRegistered",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "provider2",
				},
			},
			newProviders: []*registrypb.ProviderInfo{},
			expected: []*registrypb.MimeTypeInfo{
				{
					MimeType:           "text/json",
					Ext:                "json",
					AppProviders:       []*registrypb.ProviderInfo{},
					Name:               "JSON File",
					Icon:               "https://example.org/icons&file=json.png",
					DefaultApplication: "provider2",
				},
			},
		},
		{
			name:              "one mime type - only default provider registered",
			registryNamespace: "oneMimeTypenOnlyDefaultProviderRegistered",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "provider1",
				},
			},
			newProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "provider1",
				},
			},
			expected: []*registrypb.MimeTypeInfo{
				{
					MimeType: "text/json",
					Ext:      "json",
					AppProviders: []*registrypb.ProviderInfo{
						{
							MimeTypes: []string{"text/json"},
							Address:   "127.0.0.1:65535",
							Name:      "provider1",
						},
					},
					DefaultApplication: "provider1",
					Name:               "JSON File",
					Icon:               "https://example.org/icons&file=json.png",
				},
			},
		},
		{
			name:              "one mime type - more providers",
			registryNamespace: "oneMimeTypeMoreProviders",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "JSON_DEFAULT_PROVIDER",
				},
			},
			newProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "NOT_DEFAULT_PROVIDER",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "JSON_DEFAULT_PROVIDER",
				},
			},
			expected: []*registrypb.MimeTypeInfo{
				{
					MimeType: "text/json",
					Ext:      "json",
					AppProviders: []*registrypb.ProviderInfo{
						{
							MimeTypes: []string{"text/json"},
							Address:   "127.0.0.2:65535",
							Name:      "NOT_DEFAULT_PROVIDER",
						},
						{
							MimeTypes: []string{"text/json"},
							Address:   "127.0.0.1:65535",
							Name:      "JSON_DEFAULT_PROVIDER",
						},
					},
					DefaultApplication: "JSON_DEFAULT_PROVIDER",
					Name:               "JSON File",
					Icon:               "https://example.org/icons&file=json.png",
				},
			},
		},
		{
			name:              "multiple mime types",
			registryNamespace: "multipleMimeTypes",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "JSON_DEFAULT_PROVIDER",
				},
				{
					MimeType:   "text/xml",
					Extension:  "xml",
					Name:       "XML File",
					Icon:       "https://example.org/icons&file=xml.png",
					DefaultApp: "XML_DEFAULT_PROVIDER",
				},
			},
			newProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json", "text/xml"},
					Address:   "127.0.0.1:65535",
					Name:      "NOT_DEFAULT_PROVIDER2",
				},
				{
					MimeTypes: []string{"text/xml"},
					Address:   "127.0.0.2:65535",
					Name:      "NOT_DEFAULT_PROVIDER1",
				},
				{
					MimeTypes: []string{"text/xml", "text/json"},
					Address:   "127.0.0.3:65535",
					Name:      "JSON_DEFAULT_PROVIDER",
				},
				{
					MimeTypes: []string{"text/xml", "text/json"},
					Address:   "127.0.0.4:65535",
					Name:      "XML_DEFAULT_PROVIDER",
				},
			},
			expected: []*registrypb.MimeTypeInfo{
				{
					MimeType: "text/json",
					Ext:      "json",
					AppProviders: []*registrypb.ProviderInfo{
						{
							MimeTypes: []string{"text/json", "text/xml"},
							Address:   "127.0.0.1:65535",
							Name:      "NOT_DEFAULT_PROVIDER2",
						},
						{
							MimeTypes: []string{"text/xml", "text/json"},
							Address:   "127.0.0.3:65535",
							Name:      "JSON_DEFAULT_PROVIDER",
						},
						{
							MimeTypes: []string{"text/xml", "text/json"},
							Address:   "127.0.0.2:65535",
							Name:      "XML_DEFAULT_PROVIDER",
						},
					},
					DefaultApplication: "JSON_DEFAULT_PROVIDER",
					Name:               "JSON File",
					Icon:               "https://example.org/icons&file=json.png",
				},
				{
					MimeType: "text/xml",
					Ext:      "xml",
					AppProviders: []*registrypb.ProviderInfo{
						{
							MimeTypes: []string{"text/json", "text/xml"},
							Address:   "127.0.0.1:65535",
							Name:      "NOT_DEFAULT_PROVIDER2",
						},
						{
							MimeTypes: []string{"text/xml"},
							Address:   "127.0.0.2:65535",
							Name:      "NOT_DEFAULT_PROVIDER1",
						},
						{
							MimeTypes: []string{"text/xml", "text/json"},
							Address:   "127.0.0.3:65535",
							Name:      "JSON_DEFAULT_PROVIDER",
						},
						{
							MimeTypes: []string{"text/xml", "text/json"},
							Address:   "127.0.0.4:65535",
							Name:      "XML_DEFAULT_PROVIDER",
						},
					},
					DefaultApplication: "XML_DEFAULT_PROVIDER",
					Name:               "XML File",
					Icon:               "https://example.org/icons&file=xml.png",
				},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {

			ctx := context.TODO()

			// add all the providers
			for _, p := range tt.newProviders {
				err := registerWithMicroReg(tt.registryNamespace, p)
				if err != nil {
					t.Fatal("unexpected error creating adding new providers:", err)
				}
			}

			registry, err := New(map[string]interface{}{
				"mime_types": tt.mimeTypes,
				"namespace":  tt.registryNamespace,
			})
			if err != nil {
				t.Fatal("unexpected error creating the registry:", err)
			}

			got, err := registry.ListSupportedMimeTypes(ctx)
			if err != nil {
				t.Error("unexpected error listing supported mime types:", err)
			}

			if !mimeTypesEquals(got, tt.expected) {
				t.Errorf("mime types list different from expected: \n\tgot=%v\n\texp=%v", got, tt.expected)
			}

		})
	}
}

func TestSetDefaultProviderForMimeType(t *testing.T) {
	testCases := []struct {
		name              string
		registryNamespace string
		mimeTypes         []*mimeTypeConfig
		newDefault        struct {
			mimeType string
			provider *registrypb.ProviderInfo
		}
		newProviders []*registrypb.ProviderInfo
	}{
		{
			name:              "set new default - no new providers",
			registryNamespace: "setNewDefaultNoNewProviders",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "JSON_DEFAULT_PROVIDER",
				},
			},
			newProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "JSON_DEFAULT_PROVIDER",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "NEW_DEFAULT",
				},
			},
			newDefault: struct {
				mimeType string
				provider *registrypb.ProviderInfo
			}{
				mimeType: "text/json",
				provider: &registrypb.ProviderInfo{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "NEW_DEFAULT",
				},
			},
		},
		{
			name:              "set default - other providers (one is the previous default)",
			registryNamespace: "setDefaultOtherProvidersOneIsThePreviousDefault",
			mimeTypes: []*mimeTypeConfig{
				{
					MimeType:   "text/json",
					Extension:  "json",
					Name:       "JSON File",
					Icon:       "https://example.org/icons&file=json.png",
					DefaultApp: "JSON_DEFAULT_PROVIDER",
				},
			},
			newProviders: []*registrypb.ProviderInfo{
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.4:65535",
					Name:      "NO_DEFAULT_PROVIDER",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "NEW_DEFAULT",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.1:65535",
					Name:      "JSON_DEFAULT_PROVIDER",
				},
				{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.3:65535",
					Name:      "OTHER_PROVIDER",
				},
			},
			newDefault: struct {
				mimeType string
				provider *registrypb.ProviderInfo
			}{
				mimeType: "text/json",
				provider: &registrypb.ProviderInfo{
					MimeTypes: []string{"text/json"},
					Address:   "127.0.0.2:65535",
					Name:      "NEW_DEFAULT",
				},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {

			ctx := context.TODO()

			// add other provider to move things around internally :)
			for _, p := range tt.newProviders {
				err := registerWithMicroReg(tt.registryNamespace, p)
				if err != nil {
					t.Error("unexpected error adding a new provider:", err)
				}
			}

			registry, err := New(map[string]interface{}{
				"mime_types": tt.mimeTypes,
				"namespace":  tt.registryNamespace,
			})
			if err != nil {
				t.Error("unexpected error creating a new registry:", err)
			}

			err = registry.SetDefaultProviderForMimeType(ctx, tt.newDefault.mimeType, tt.newDefault.provider)
			if err != nil {
				t.Error("unexpected error setting a default provider for mime type:", err)
			}

			// check if the new default is the one set
			got, err := registry.GetDefaultProviderForMimeType(ctx, tt.newDefault.mimeType)
			if err != nil {
				t.Error("unexpected error getting the default app provider:", err)
			}

			if !equalsProviderInfo(got, tt.newDefault.provider) {
				t.Errorf("provider differ from expected:\n\tgot=%v\n\texp=%v", got, tt.newDefault.provider)
			}

		})
	}
}

func equalsMimeTypeInfo(m1, m2 *registrypb.MimeTypeInfo) bool {
	return m1.Description == m2.Description &&
		m1.AllowCreation == m2.AllowCreation &&
		providersEquals(m1.AppProviders, m2.AppProviders) &&
		m1.Ext == m2.Ext &&
		m1.MimeType == m2.MimeType &&
		m1.Name == m2.Name &&
		m1.DefaultApplication == m2.DefaultApplication
}

func mimeTypesEquals(l1, l2 []*registrypb.MimeTypeInfo) bool {
	if len(l1) != len(l2) {
		return false
	}

	if len(l1) < 1 && len(l2) < 1 {
		return true
	}

	for _, left := range l1 {
		for _, right := range l2 {
			if equalsMimeTypeInfo(left, right) {
				return true
			}
		}
	}

	return false
}

// check that all providers in the two lists are equals
func providersEquals(pi1, pi2 []*registrypb.ProviderInfo) bool {
	if len(pi1) != len(pi2) {
		return false
	}

	if len(pi1) < 1 && len(pi2) < 1 {
		return true
	}

	for _, left := range pi1 {
		for _, right := range pi2 {
			if equalsProviderInfo(left, right) {
				return true
			}
		}
	}

	return false
}

// This is to mock registering with the go-micro registry and at the same time the reference implementation
func registerWithMicroReg(ns string, p *registrypb.ProviderInfo) error {
	o := oreg.Inmemory()

	reg := oreg.GetRegistry(o)

	serviceID := ns + ".api.app-provider"

	node := &mreg.Node{
		Id:       serviceID + "-" + uuid.New().String(),
		Address:  p.Address,
		Metadata: make(map[string]string),
	}

	node.Metadata["registry"] = reg.String()
	node.Metadata["server"] = "grpc"
	node.Metadata["transport"] = "grpc"
	node.Metadata["protocol"] = "grpc"

	node.Metadata[ns+".app-provider.mime_type"] = joinMimeTypes(p.MimeTypes)
	node.Metadata[ns+".app-provider.name"] = p.Name
	node.Metadata[ns+".app-provider.description"] = p.Description
	node.Metadata[ns+".app-provider.icon"] = p.Icon

	node.Metadata[ns+".app-provider.allow_creation"] = registrypb.ProviderInfo_Capability_name[int32(p.Capability)]
	node.Metadata[ns+".app-provider.priority"] = getPriority(p)
	if p.DesktopOnly {
		node.Metadata[ns+".app-provider.desktop_only"] = "true"
	}

	service := &mreg.Service{
		Name: serviceID,
		//Version:   version,
		Nodes:     []*mreg.Node{node},
		Endpoints: make([]*mreg.Endpoint, 0),
	}

	rOpts := []mreg.RegisterOption{mreg.RegisterTTL(time.Minute)}
	if err := reg.Register(service, rOpts...); err != nil {
		return err
	}

	return nil
}
