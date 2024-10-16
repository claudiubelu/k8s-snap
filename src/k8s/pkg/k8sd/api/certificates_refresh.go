package api

import (
	"crypto/x509/pkix"
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/http"
	"path/filepath"

	apiv1 "github.com/canonical/k8s-snap-api/api/v1"
	databaseutil "github.com/canonical/k8s/pkg/k8sd/database/util"
	"github.com/canonical/k8s/pkg/k8sd/pki"
	"github.com/canonical/k8s/pkg/k8sd/setup"
	"github.com/canonical/k8s/pkg/log"
	"github.com/canonical/k8s/pkg/snap"
	snaputil "github.com/canonical/k8s/pkg/snap/util"
	"github.com/canonical/k8s/pkg/utils"
	pkiutil "github.com/canonical/k8s/pkg/utils/pki"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/microcluster/v2/state"
	"golang.org/x/sync/errgroup"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (e *Endpoints) postRefreshCertsPlan(s state.State, r *http.Request) response.Response {
	seed := rand.Intn(math.MaxInt)

	snap := e.provider.Snap()
	isWorker, err := snaputil.IsWorker(snap)
	if err != nil {
		return response.InternalError(fmt.Errorf("failed to check if node is a worker: %w", err))
	}
	if isWorker {
		return response.SyncResponse(true, apiv1.RefreshCertificatesPlanResponse{
			Seed: seed,
			CertificateSigningRequests: []string{
				fmt.Sprintf("k8sd-%d-worker-kubelet-serving", seed),
				fmt.Sprintf("k8sd-%d-worker-kubelet-client", seed),
				fmt.Sprintf("k8sd-%d-worker-kube-proxy-client", seed),
			},
		})
	}

	return response.SyncResponse(true, apiv1.RefreshCertificatesPlanResponse{
		Seed: seed,
	})

}

func (e *Endpoints) postRefreshCertsRun(s state.State, r *http.Request) response.Response {
	snap := e.provider.Snap()
	isWorker, err := snaputil.IsWorker(snap)
	if err != nil {
		return response.InternalError(fmt.Errorf("failed to check if node is a worker: %w", err))
	}
	if isWorker {
		return refreshCertsRunWorker(s, r, snap)
	}
	// TODO: Control Plane refresh
	return response.InternalError(fmt.Errorf("not implemented yet"))
}

// refreshCertsRunWorker refreshes the certificates for a worker node
func refreshCertsRunWorker(s state.State, r *http.Request, snap snap.Snap) response.Response {
	log := log.FromContext(r.Context())

	req := apiv1.RefreshCertificatesRunRequest{}
	if err := utils.NewStrictJSONDecoder(r.Body).Decode(&req); err != nil {
		return response.BadRequest(fmt.Errorf("failed to parse request: %w", err))
	}

	client, err := snap.KubernetesNodeClient("")
	if err != nil {
		return response.InternalError(fmt.Errorf("failed to get Kubernetes client: %w", err))
	}

	var certificates pki.WorkerNodePKI

	clusterConfig, err := databaseutil.GetClusterConfig(r.Context(), s)
	if err != nil {
		return response.InternalError(fmt.Errorf("failed to get cluster configuration: %w", err))
	}

	if clusterConfig.Certificates.CACert == nil || clusterConfig.Certificates.ClientCACert == nil {
		return response.InternalError(fmt.Errorf("missing CA certificates"))
	}

	certificates.CACert = clusterConfig.Certificates.GetCACert()
	certificates.ClientCACert = clusterConfig.Certificates.GetClientCACert()

	g, ctx := errgroup.WithContext(r.Context())

	for _, csr := range []struct {
		name         string
		commonName   string
		organization []string
		usages       []certv1.KeyUsage
		hostnames    []string
		ips          []net.IP
		signerName   string
		certificate  *string
		key          *string
	}{
		{
			name:         fmt.Sprintf("k8sd-%d-worker-kubelet-serving", req.Seed),
			commonName:   fmt.Sprintf("system:node:%s", snap.Hostname()),
			organization: []string{"system:nodes"},
			usages:       []certv1.KeyUsage{certv1.UsageDigitalSignature, certv1.UsageKeyEncipherment, certv1.UsageServerAuth},
			hostnames:    []string{snap.Hostname()},
			ips:          []net.IP{net.ParseIP(s.Address().Hostname())},
			signerName:   "k8sd.io/kubelet-serving",
			certificate:  &certificates.KubeletCert,
			key:          &certificates.KubeletKey,
		},
		{
			name:         fmt.Sprintf("k8sd-%d-worker-kubelet-client", req.Seed),
			commonName:   fmt.Sprintf("system:node:%s", snap.Hostname()),
			organization: []string{"system:nodes"},
			usages:       []certv1.KeyUsage{certv1.UsageDigitalSignature, certv1.UsageKeyEncipherment, certv1.UsageClientAuth},
			signerName:   "k8sd.io/kubelet-client",
			certificate:  &certificates.KubeletClientCert,
			key:          &certificates.KubeletClientKey,
		},
		{
			name:        fmt.Sprintf("k8sd-%d-worker-kube-proxy-client", req.Seed),
			commonName:  "system:kube-proxy",
			usages:      []certv1.KeyUsage{certv1.UsageDigitalSignature, certv1.UsageKeyEncipherment, certv1.UsageClientAuth},
			signerName:  "k8sd.io/kube-proxy-client",
			certificate: &certificates.KubeProxyClientCert,
			key:         &certificates.KubeProxyClientKey,
		},
	} {
		csr := csr
		g.Go(func() error {
			csrPEM, keyPEM, err := pkiutil.GenerateCSR(
				pkix.Name{
					CommonName:   csr.commonName,
					Organization: csr.organization,
				},
				2048,
				csr.hostnames,
				csr.ips,
			)
			if err != nil {
				return fmt.Errorf("failed to generate CSR for %s: %w", csr.name, err)
			}

			if _, err = client.CertificatesV1().CertificateSigningRequests().Create(ctx, &certv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: csr.name,
				},
				Spec: certv1.CertificateSigningRequestSpec{
					Request:    []byte(csrPEM),
					Usages:     csr.usages,
					SignerName: csr.signerName,
				},
			}, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("failed to create CSR for %s: %w", csr.name, err)
			}

			if err := client.WatchCertificateSigningRequest(
				ctx,
				csr.name,
				func(request *certv1.CertificateSigningRequest) (bool, error) {
					return verifyCSRAndSetPKI(request, keyPEM, csr.certificate, csr.key)
				},
			); err != nil {
				log.Error(err, "Failed to watch CSR")
				return fmt.Errorf("certificate signing request failed: %w", err)
			}

			return nil

		})

	}

	if err := g.Wait(); err != nil {
		return response.InternalError(fmt.Errorf("failed to get worker node certificates: %w", err))
	}

	if _, err = setup.EnsureWorkerPKI(snap, &certificates); err != nil {
		return response.InternalError(fmt.Errorf("failed to write worker PKI: %w", err))
	}

	// Kubeconfigs
	if err := setup.Kubeconfig(filepath.Join(snap.KubernetesConfigDir(), "kubelet.conf"), "127.0.0.1:6443", certificates.CACert, certificates.KubeletClientCert, certificates.KubeletClientKey); err != nil {
		return response.InternalError(fmt.Errorf("failed to generate kubelet kubeconfig: %w", err))
	}
	if err := setup.Kubeconfig(filepath.Join(snap.KubernetesConfigDir(), "proxy.conf"), "127.0.0.1:6443", certificates.CACert, certificates.KubeProxyClientCert, certificates.KubeProxyClientKey); err != nil {
		return response.InternalError(fmt.Errorf("failed to generate kube-proxy kubeconfig: %w", err))
	}

	// Restart the services
	if err := snap.RestartService(r.Context(), "kubelet"); err != nil {
		return response.InternalError(fmt.Errorf("failed to restart kubelet: %w", err))
	}
	if err := snap.RestartService(r.Context(), "kube-proxy"); err != nil {
		return response.InternalError(fmt.Errorf("failed to restart kube-proxy: %w", err))
	}

	cert, _, err := pkiutil.LoadCertificate(certificates.KubeletCert, "")
	if err != nil {
		return response.InternalError(fmt.Errorf("failed to load kubelet certificate: %w", err))
	}

	expirationDuration := cert.NotAfter.Sub(cert.NotBefore)
	return response.SyncResponse(true, apiv1.RefreshCertificatesRunResponse{
		ExpirationSeconds: int(expirationDuration.Seconds()),
	})

}

// isCertificateSigningRequestApprovedAndIssued checks if the certificate
// signing request is approved and issued. It returns true if the CSR is
// approved and issued, false if it is pending, and an error if it is denied
// or failed.
func isCertificateSigningRequestApprovedAndIssued(csr *certv1.CertificateSigningRequest) (bool, error) {
	for _, condition := range csr.Status.Conditions {
		if condition.Type == certv1.CertificateApproved && condition.Status == corev1.ConditionTrue {
			return len(csr.Status.Certificate) > 0, nil

		}
		if condition.Type == certv1.CertificateDenied && condition.Status == corev1.ConditionTrue {
			return false, fmt.Errorf(":CSR %s was denied: %s", csr.Name, condition.Reason)
		}
		if condition.Type == certv1.CertificateFailed && condition.Status == corev1.ConditionTrue {
			return false, fmt.Errorf("CSR %s failed: %s", csr.Name, condition.Reason)
		}
	}
	return false, nil
}

// verifyCSRAndSetPKI verifies the certificate signing request and sets the
// certificate and key if the CSR is approved.
func verifyCSRAndSetPKI(csr *certv1.CertificateSigningRequest, keyPEM string, certificate, key *string) (bool, error) {
	approved, err := isCertificateSigningRequestApprovedAndIssued(csr)
	if err != nil {
		return false, fmt.Errorf("failed to validate csr: %w", err)
	}

	if !approved {
		return false, nil
	}

	if _, _, err = pkiutil.LoadCertificate(string(csr.Status.Certificate), ""); err != nil {
		return false, fmt.Errorf("failed to load certificate: %w", err)
	}

	*certificate = string(csr.Status.Certificate)
	*key = keyPEM
	return true, nil
}
