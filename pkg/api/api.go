package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Service ChmlFrp api servie
type Service struct {
	Host url.URL
}

// NewService crate ChmlFrp api servie
func NewService(host string) (s *Service, err error) {
	u, err := url.Parse(host)
	if err != nil {
		return
	}
	return &Service{*u}, nil
}

// 简单启动获取Cfg
func (s Service) EZStartGetCfg(token string, proxyid string) (cfg string, err error) {
	values := url.Values{}
	values.Set("action", "getcfg")
	values.Set("token", token)
	values.Set("id", proxyid)
	// Encode 请求参数
	s.Host.RawQuery = values.Encode()
	defer func(u *url.URL) {
		u.RawQuery = ""
	}(&s.Host)

	tr := &http.Transport{
		// 跳过证书验证
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: tr}

	resp, err := client.Get(s.Host.String())
	// 请求出现错误，resp返回nil判断
	if resp == nil {
		return "", err
	}

	defer resp.Body.Close()
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", ErrHTTPStatus{
			Status: resp.StatusCode,
			Text:   resp.Status,
		}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	response := ResGetCfg{}
	if err = json.Unmarshal(body, &response); err != nil {
		return "", err
	}
	if !response.Success {
		return "", ErrCheckTokenFail{response.Message}
	}
	return response.Cfg, nil
}

type ErrHTTPStatus struct {
	Status int    `json:"status"`
	Text   string `json:"message"`
}

func (e ErrHTTPStatus) Error() string {
	return fmt.Sprintf("ChmlFrp API Error (Status: %d, Text: %s)", e.Status, e.Text)
}

type ResGetCfg struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Cfg     string `json:"cfg"`
}

type ErrCheckTokenFail struct {
	Message string
}

func (e ErrCheckTokenFail) Error() string {
	return e.Message
}
