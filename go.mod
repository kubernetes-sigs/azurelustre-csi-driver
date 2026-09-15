module sigs.k8s.io/azurelustre-csi-driver

go 1.25.0

toolchain go1.25.13

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.22.0
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.14.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6 v6.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4 v4.0.0
	github.com/Azure/go-autorest/autorest v0.11.30
	github.com/container-storage-interface/spec v1.11.0
	github.com/kubernetes-csi/csi-lib-utils v0.19.0
	github.com/kubernetes-csi/csi-test/v5 v5.3.1
	github.com/pborman/uuid v1.2.1
	github.com/pelletier/go-toml v1.9.5
	github.com/stretchr/testify v1.11.1
	go.uber.org/mock v0.6.0
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12
	k8s.io/api v0.34.10
	k8s.io/apimachinery v0.34.10
	k8s.io/client-go v1.5.2
	k8s.io/klog/v2 v2.140.0
	k8s.io/kubernetes v1.34.10
	k8s.io/mount-utils v0.32.13
	k8s.io/utils v0.0.0-20260707023825-cf1189d6abe3
	sigs.k8s.io/cloud-provider-azure v1.32.20
	sigs.k8s.io/cloud-provider-azure/pkg/azclient/configloader v0.5.3
	sigs.k8s.io/yaml v1.6.0
)

require (
	cyphar.com/go-pathrs v0.2.5 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.12.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2 v2.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6 v6.4.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6 v6.5.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault v1.5.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns v1.3.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage v1.8.1 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets v1.3.1 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/internal v1.1.1 // indirect
	github.com/Azure/go-autorest v14.2.0+incompatible // indirect
	github.com/Azure/go-autorest/autorest/adal v0.9.24 // indirect
	github.com/Azure/go-autorest/autorest/date v0.3.1 // indirect
	github.com/Azure/go-autorest/logger v0.2.2 // indirect
	github.com/Azure/go-autorest/tracing v0.6.1 // indirect
	github.com/Azure/msi-dataplane v0.4.3 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.8.0 // indirect
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cyphar/filepath-securejoin v0.6.1 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.27.3 // indirect
	github.com/go-openapi/swag/cmdutils v0.27.3 // indirect
	github.com/go-openapi/swag/conv v0.27.3 // indirect
	github.com/go-openapi/swag/fileutils v0.27.3 // indirect
	github.com/go-openapi/swag/jsonutils v0.27.3 // indirect
	github.com/go-openapi/swag/loading v0.27.3 // indirect
	github.com/go-openapi/swag/mangling v0.27.3 // indirect
	github.com/go-openapi/swag/netutils v0.27.3 // indirect
	github.com/go-openapi/swag/pools v0.27.3 // indirect
	github.com/go-openapi/swag/stringutils v0.27.3 // indirect
	github.com/go-openapi/swag/typeutils v0.27.3 // indirect
	github.com/go-openapi/swag/yamlutils v0.27.3 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/onsi/ginkgo/v2 v2.27.5 // indirect
	github.com/onsi/gomega v1.38.3 // indirect
	github.com/opencontainers/selinux v1.13.1 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.19.2 // indirect
	github.com/samber/lo v1.52.0 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.61.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/sdk v1.43.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/exp v0.0.0-20260813180055-c1d0aacb2297 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.11.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260810153831-ec0a7760b754 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.12.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/apiextensions-apiserver v0.31.14 // indirect
	k8s.io/apiserver v0.34.10 // indirect
	k8s.io/cloud-provider v0.32.13 // indirect
	k8s.io/component-base v0.34.10 // indirect
	k8s.io/component-helpers v0.34.10 // indirect
	k8s.io/controller-manager v0.34.10 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	sigs.k8s.io/cloud-provider-azure/pkg/azclient v0.6.2 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
)

replace k8s.io/api => k8s.io/api v0.34.10

replace k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.34.10

replace k8s.io/apimachinery => k8s.io/apimachinery v0.34.10

replace k8s.io/apiserver => k8s.io/apiserver v0.34.10

replace k8s.io/cli-runtime => k8s.io/cli-runtime v0.34.10

replace k8s.io/client-go => k8s.io/client-go v0.34.10

replace k8s.io/cloud-provider => k8s.io/cloud-provider v0.34.10

replace k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.34.10

replace k8s.io/code-generator => k8s.io/code-generator v0.34.10

replace k8s.io/component-base => k8s.io/component-base v0.34.10

replace k8s.io/component-helpers => k8s.io/component-helpers v0.34.10

replace k8s.io/controller-manager => k8s.io/controller-manager v0.34.10

replace k8s.io/cri-api => k8s.io/cri-api v0.34.10

replace k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.34.10

replace k8s.io/dynamic-resource-allocation => k8s.io/dynamic-resource-allocation v0.34.10

replace k8s.io/kms => k8s.io/kms v0.34.10

replace k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.34.10

replace k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.34.10

replace k8s.io/kube-proxy => k8s.io/kube-proxy v0.34.10

replace k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.34.10

replace k8s.io/kubectl => k8s.io/kubectl v0.34.10

replace k8s.io/kubelet => k8s.io/kubelet v0.34.10

replace k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.30.4

replace k8s.io/metrics => k8s.io/metrics v0.34.10

replace k8s.io/mount-utils => k8s.io/mount-utils v0.34.10

replace k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.34.10

replace k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.34.10

replace k8s.io/sample-cli-plugin => k8s.io/sample-cli-plugin v0.34.10

replace k8s.io/sample-controller => k8s.io/sample-controller v0.34.10

replace k8s.io/endpointslice => k8s.io/endpointslice v0.34.10

replace k8s.io/cri-client => k8s.io/cri-client v0.34.10

replace k8s.io/externaljwt => k8s.io/externaljwt v0.34.10
