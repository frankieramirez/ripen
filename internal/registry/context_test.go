package registry

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/frankieramirez/ripen/internal/domain"
)

type cancellationTransport struct{}

func (cancellationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestRegistryRequestsHonorCallerCancellation(t *testing.T) {
	client := New(WithHTTPClient(&http.Client{Transport: cancellationTransport{}}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.WithContext(ctx).ResolveDigest(domain.ImageReference{Registry: "example.com", Repository: "app", Tag: "latest"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if client.ctx.Err() != nil {
		t.Fatal("original client was canceled")
	}
}
