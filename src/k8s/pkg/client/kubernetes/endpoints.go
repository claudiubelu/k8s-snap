package kubernetes

import (
	"context"
	"fmt"

	"github.com/canonical/k8s/pkg/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// GetKubeAPIServerEndpoints retrieves the known kube-apiserver endpoints of the cluster.
// GetKubeAPIServerEndpoints returns an error if the list of endpoints is empty.
func (c *Client) GetKubeAPIServerEndpoints(ctx context.Context) ([]string, error) {
	var endpoints *v1.Endpoints
	var err error
	err = retry.OnError(retry.DefaultBackoff, func(err error) bool { return true }, func() error {
		endpoints, err = c.CoreV1().Endpoints("default").Get(ctx, "kubernetes", metav1.GetOptions{})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoints for kubernetes service: %w", err)
	}
	if endpoints == nil {
		return nil, fmt.Errorf("endpoints for kubernetes service not found")
	}

	addresses := utils.ParseEndpoints(endpoints)
	if len(addresses) == 0 {
		return nil, fmt.Errorf("empty list of endpoints for the kubernetes service")
	}

	return addresses, nil
}
