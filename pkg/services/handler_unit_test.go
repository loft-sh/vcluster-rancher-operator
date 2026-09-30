package services

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNamespace   = "c-m-abcde"
	testSecretName  = "crt-token-default-token"
	testToken       = "exampletokenexampletokenexampletokenexampletoken1234567"
	testInsecureCmd = "curl --insecure -sfL https://rancher.example.com/v3/import/{token}_c-m-abcde.yaml | kubectl apply -f -"
)

// registrationTokenObject builds a cluster registration token with the given status fields.
func registrationTokenObject(status map[string]any) v1unstructured.Unstructured {
	obj := v1unstructured.Unstructured{Object: map[string]any{"status": status}}
	obj.SetName("default-token")
	obj.SetNamespace(testNamespace)
	return obj
}

// handlerWithSecrets builds a handler whose local client serves the given secrets.
func handlerWithSecrets(t *testing.T, secrets ...*corev1.Secret) *Handler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}

	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, secret := range secrets {
		builder = builder.WithObjects(secret)
	}

	return &Handler{
		Ctx:                     context.Background(),
		LocalUnstructuredClient: unstructured.Client{Client: builder.Build()},
	}
}

func tokenSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace},
		Data:       data,
	}
}

func TestResolveRegistrationCommandFromStatusToken(t *testing.T) {
	handler := handlerWithSecrets(t)

	token := registrationTokenObject(map[string]any{
		"token":           testToken,
		"insecureCommand": testInsecureCmd,
	})

	command, err := handler.resolveRegistrationCommand(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(command, tokenPlaceholder) {
		t.Errorf("command still contains %q: %s", tokenPlaceholder, command)
	}

	if !strings.Contains(command, testToken) {
		t.Errorf("command does not contain the token: %s", command)
	}
}

func TestResolveRegistrationCommandFromTokenSecret(t *testing.T) {
	handler := handlerWithSecrets(t, tokenSecret(map[string][]byte{"token": []byte(testToken)}))

	// rancher 2.15 and later leave status.token empty and reference a secret instead
	token := registrationTokenObject(map[string]any{
		"token":           "",
		"tokenSecretName": testSecretName,
		"insecureCommand": testInsecureCmd,
	})

	command, err := handler.resolveRegistrationCommand(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := strings.ReplaceAll(testInsecureCmd, tokenPlaceholder, testToken)
	if command != expected {
		t.Errorf("got %q, want %q", command, expected)
	}
}

func TestResolveRegistrationCommandWithoutPlaceholder(t *testing.T) {
	handler := handlerWithSecrets(t)

	command := strings.ReplaceAll(testInsecureCmd, tokenPlaceholder, testToken)
	token := registrationTokenObject(map[string]any{"insecureCommand": command})

	// no token lookup should be needed, the command is already complete
	got, err := handler.resolveRegistrationCommand(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != command {
		t.Errorf("got %q, want %q", got, command)
	}
}

func TestResolveRegistrationCommandPending(t *testing.T) {
	tests := []struct {
		name    string
		status  map[string]any
		secrets []*corev1.Secret
	}{
		{
			name:   "no insecure command",
			status: map[string]any{"token": testToken},
		},
		{
			name:   "no token and no token secret name",
			status: map[string]any{"insecureCommand": testInsecureCmd},
		},
		{
			name:   "token secret does not exist yet",
			status: map[string]any{"tokenSecretName": testSecretName, "insecureCommand": testInsecureCmd},
		},
		{
			name:    "token secret has no token key",
			status:  map[string]any{"tokenSecretName": testSecretName, "insecureCommand": testInsecureCmd},
			secrets: []*corev1.Secret{tokenSecret(map[string][]byte{"expiresAt": []byte("2026-10-31T04:52:45Z")})},
		},
		{
			name:    "token secret has a blank token",
			status:  map[string]any{"tokenSecretName": testSecretName, "insecureCommand": testInsecureCmd},
			secrets: []*corev1.Secret{tokenSecret(map[string][]byte{"token": []byte("  \n")})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := handlerWithSecrets(t, tt.secrets...)

			command, err := handler.resolveRegistrationCommand(registrationTokenObject(tt.status))
			if !errors.Is(err, ErrRancherResourcesPending) {
				t.Errorf("got error %v, want ErrRancherResourcesPending", err)
			}

			if command != "" {
				t.Errorf("got command %q, want empty", command)
			}
		})
	}
}

func TestRegistrationTokenTrimsWhitespace(t *testing.T) {
	handler := handlerWithSecrets(t, tokenSecret(map[string][]byte{"token": []byte(testToken + "\n")}))

	token := registrationTokenObject(map[string]any{"tokenSecretName": testSecretName})

	got, err := handler.registrationToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != testToken {
		t.Errorf("got %q, want %q", got, testToken)
	}
}

func TestDecodeRegistrationTokenRejectsInvalidBase64(t *testing.T) {
	const invalid = "not-base64!!"

	// the fake client stores secret data as bytes, so an invalid encoding is injected directly
	if _, err := base64.StdEncoding.DecodeString(invalid); err == nil {
		t.Fatal("expected the test fixture to be invalid base64")
	}

	secret := v1unstructured.Unstructured{Object: map[string]any{
		"data": map[string]any{"token": invalid},
	}}

	_, err := decodeRegistrationToken(secret, testSecretName)
	if err == nil {
		t.Fatal("expected an error for invalid base64, got none")
	}

	if errors.Is(err, ErrRancherResourcesPending) {
		t.Error("invalid base64 should not be reported as pending, it will never resolve")
	}
}

func TestAgentInstallScript(t *testing.T) {
	command := strings.ReplaceAll(testInsecureCmd, tokenPlaceholder, testToken)

	script := agentInstallScript("10.43.0.10", command)

	for _, want := range []string{
		"set -o pipefail",
		"sed 's/localhost:8443/10.43.0.10:443/g'",
		"kubectl --kubeconfig /tmp/vcluster-kc.yaml apply -f -",
		"curl --insecure -fL --show-error",
		testToken,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script does not contain %q:\n%s", want, script)
		}
	}

	// the silent curl flags must be gone, they hide http errors from the job log
	if strings.Contains(script, "-sfL") {
		t.Errorf("script still uses silent curl flags:\n%s", script)
	}
}
