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
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/fatedier/frp/pkg/auth"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
	netpkg "github.com/fatedier/frp/pkg/util/net"
	"github.com/fatedier/frp/pkg/util/version"
)

type controlSessionDialer struct {
	ctx context.Context

	common *v1.ClientCommonConfig
	auth   *auth.ClientAuth

	connectorCreator func(context.Context, *v1.ClientCommonConfig) Connector
}

func (d *controlSessionDialer) Dial(previousRunID string) (*SessionContext, error) {
	connector := d.connectorCreator(d.ctx, d.common)
	if err := connector.Open(); err != nil {
		return nil, err
	}

	success := false
	defer func() {
		if !success {
			_ = connector.Close()
		}
	}()

	conn, err := connector.Connect()
	if err != nil {
		return nil, err
	}
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()

	loginMsg, err := d.buildLoginMsg(previousRunID)
	if err != nil {
		return nil, err
	}

	loginRespMsg, err := d.exchangeLogin(conn, loginMsg)
	if err != nil {
		return nil, err
	}
	if loginRespMsg.Error != "" {
		return nil, errors.New(loginRespMsg.Error)
	}

	controlRW, err := netpkg.NewCryptoReadWriter(conn, d.auth.EncryptionKey())
	if err != nil {
		return nil, fmt.Errorf("create control crypto read writer: %w", err)
	}

	success = true
	return &SessionContext{
		Common:    d.common,
		RunID:     loginRespMsg.RunID,
		Conn:      msg.NewConn(conn, msg.NewReadWriter(controlRW, d.common.Transport.WireProtocol)),
		Auth:      d.auth,
		Connector: newMessageConnector(connector, d.common.Transport.WireProtocol),
	}, nil
}

func (d *controlSessionDialer) buildLoginMsg(previousRunID string) (*msg.Login, error) {
	hostname, _ := os.Hostname()
	loginMsg := &msg.Login{
		Arch:      runtime.GOARCH,
		Os:        runtime.GOOS,
		Hostname:  hostname,
		PoolCount: d.common.Transport.PoolCount,
		User:      d.common.User,
		Version:   version.Full(),
		Timestamp: time.Now().Unix(),
		RunID:     previousRunID,
		Metas:     d.common.Metadatas,
	}

	if err := d.auth.Setter.SetLogin(loginMsg); err != nil {
		return nil, err
	}
	return loginMsg, nil
}

func (d *controlSessionDialer) exchangeLogin(conn net.Conn, loginMsg *msg.Login) (*msg.LoginResp, error) {
	rw := msg.NewV1ReadWriter(conn)
	if err := rw.WriteMsg(loginMsg); err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	var loginRespMsg msg.LoginResp
	if err := rw.ReadMsgInto(&loginRespMsg); err != nil {
		return nil, err
	}
	return &loginRespMsg, nil
}
