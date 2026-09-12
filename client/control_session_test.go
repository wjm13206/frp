// Copyright 2026 The frp Authors
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

package client

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fatedier/frp/pkg/auth"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
)

type testConnector struct {
	conn   net.Conn
	closed atomic.Bool
}

func (c *testConnector) Open() error {
	return nil
}

func (c *testConnector) Connect() (net.Conn, error) {
	return c.conn, nil
}

func (c *testConnector) Close() error {
	c.closed.Store(true)
	return nil
}

type trackingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *trackingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func newTestControlSessionDialer(t *testing.T, connector Connector) *controlSessionDialer {
	t.Helper()

	authRuntime, err := auth.BuildClientAuth(&v1.AuthClientConfig{
		Method: v1.AuthMethodToken,
		Token:  "token",
	})
	require.NoError(t, err)

	return &controlSessionDialer{
		ctx: context.Background(),
		common: &v1.ClientCommonConfig{
			User: "test-user",
			Transport: v1.ClientTransportConfig{
				Protocol:     "tcp",
				WireProtocol: "v1",
			},
		},
		auth: authRuntime,
		connectorCreator: func(context.Context, *v1.ClientCommonConfig) Connector {
			return connector
		},
	}
}

func TestControlSessionDialerDialV1(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	defer serverRaw.Close()

	connector := &testConnector{conn: &trackingConn{Conn: clientRaw}}
	serverErrCh := make(chan error, 1)
	go func() {
		rw := msg.NewV1ReadWriter(serverRaw)
		var loginMsg msg.Login
		if err := rw.ReadMsgInto(&loginMsg); err != nil {
			serverErrCh <- err
			return
		}
		if loginMsg.RunID != "previous-run-id" {
			serverErrCh <- fmt.Errorf("unexpected previous run id: %s", loginMsg.RunID)
			return
		}
		if loginMsg.User != "test-user" {
			serverErrCh <- fmt.Errorf("unexpected user: %s", loginMsg.User)
			return
		}
		serverErrCh <- rw.WriteMsg(&msg.LoginResp{RunID: "run-v1"})
	}()

	dialer := newTestControlSessionDialer(t, connector)
	sessionCtx, err := dialer.Dial("previous-run-id")
	require.NoError(t, err)
	defer sessionCtx.Conn.Close()
	defer sessionCtx.Connector.Close()

	require.Equal(t, "run-v1", sessionCtx.RunID)
	require.NotNil(t, sessionCtx.Conn)
	require.NotNil(t, sessionCtx.Connector)
	require.False(t, connector.closed.Load())
	require.NoError(t, <-serverErrCh)
}

func TestControlSessionDialerDialLoginErrorClosesResources(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	defer serverRaw.Close()

	clientConn := &trackingConn{Conn: clientRaw}
	connector := &testConnector{conn: clientConn}
	serverErrCh := make(chan error, 1)
	go func() {
		rw := msg.NewV1ReadWriter(serverRaw)
		var loginMsg msg.Login
		if err := rw.ReadMsgInto(&loginMsg); err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- rw.WriteMsg(&msg.LoginResp{Error: "login denied"})
	}()

	dialer := newTestControlSessionDialer(t, connector)
	sessionCtx, err := dialer.Dial("")
	require.Nil(t, sessionCtx)
	require.ErrorContains(t, err, "login denied")
	require.True(t, clientConn.closed.Load())
	require.True(t, connector.closed.Load())
	require.NoError(t, <-serverErrCh)
}
