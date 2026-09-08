package portainer

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type cancellationTransport struct{}

func (cancellationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestBackendClonePropagatesCancellationToPortainer(t *testing.T) {
	client := &httpsClient{ctx: context.Background(), base: "https://example.com", httpClient: &http.Client{Transport: cancellationTransport{}}, timeout: time.Minute}
	original := NewBackend(&Adapter{client: client}, "ripen")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := original.WithContext(ctx).Preflight()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if client.ctx.Err() != nil {
		t.Fatal("original client was canceled")
	}
}
