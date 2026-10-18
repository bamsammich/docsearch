package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/httpx"
)

// MCPProxySuite covers /mcp reached through a reverse proxy on the same
// machine: a loopback listener answering a request addressed to another host.
type MCPProxySuite struct {
	suite.Suite
	server *httptest.Server
}

func TestMCPProxy(t *testing.T) { suite.Run(t, new(MCPProxySuite)) }

const proxiedHost = "docs.tailnet.example:18765"

func (s *MCPProxySuite) SetupTest() {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	handler := mcpHandler(srv, slog.New(slog.DiscardHandler))
	allowed := []string{"http://" + proxiedHost}
	s.server = httptest.NewServer(httpx.RequireAllowedOrigin(allowed, handler))
	s.T().Cleanup(s.server.Close)
}

func (s *MCPProxySuite) post(origin string) int {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, err := http.NewRequestWithContext(
		s.T().Context(),
		http.MethodPost,
		s.server.URL,
		strings.NewReader(body),
	)
	s.Require().NoError(err)
	req.Host = proxiedHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", origin)
	resp, err := s.server.Client().Do(req)
	s.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, resp.Body)
	s.Require().NoError(err)
	return resp.StatusCode
}

func (s *MCPProxySuite) TestAProxiedRequestFromAnAllowedOriginIsServed() {
	s.Equal(http.StatusOK, s.post("http://"+proxiedHost))
}

func (s *MCPProxySuite) TestARebindingOriginIsStillRefused() {
	s.Equal(http.StatusForbidden, s.post("http://rebinding.example"))
}

// A sandboxed iframe or a data: page sends the literal origin "null". It is
// present and on no list, so it is refused, not read as an absent Origin.
func (s *MCPProxySuite) TestTheNullOriginIsRefused() {
	s.Equal(http.StatusForbidden, s.post("null"))
}
