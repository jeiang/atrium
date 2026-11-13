package agentclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

type StreamRequest struct {
	Resource                                     string
	Path                                         protocol.Path
	Method                                       string
	Range, IfRange, IfNoneMatch, IfModifiedSince string
}

func (c *Client) Stream(ctx context.Context, in StreamRequest) (*http.Response, error) {
	if in.Method != http.MethodGet && in.Method != http.MethodHead {
		return nil, errors.New("unsupported media stream method")
	}
	resource := "/v1/files"
	switch in.Resource {
	case "file":
	case "thumbnail":
		resource = "/v1/thumbnail"
	default:
		return nil, errors.New("unsupported media resource")
	}
	q := url.Values{"root": {in.Path.Root}, "path": {in.Path.Rel}}
	req, err := http.NewRequestWithContext(ctx, in.Method, c.base+resource+"?"+q.Encode(), nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	for name, value := range map[string]string{"Range": in.Range, "If-Range": in.IfRange, "If-None-Match": in.IfNoneMatch, "If-Modified-Since": in.IfModifiedSince} {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	return c.do(req)
}
