package model

import (
	"fmt"
	"strings"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

type VisitorDefinition struct {
	Name string `json:"name"`
	Type string `json:"type"`

	STCP *v1.STCPVisitorConfig `json:"stcp,omitempty"`
	SUDP *v1.SUDPVisitorConfig `json:"sudp,omitempty"`
	XTCP *v1.XTCPVisitorConfig `json:"xtcp,omitempty"`
}

func (p *VisitorDefinition) Validate(pathName string, isUpdate bool) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("访问者名称不能为空")
	}
	if !IsVisitorType(p.Type) {
		return fmt.Errorf("不支持的访问者类型: %s", p.Type)
	}
	if isUpdate && pathName != "" && pathName != p.Name {
		return fmt.Errorf("URL 中的访问者名称必须与请求体中的名称一致")
	}

	_, blockType, blockCount := p.activeBlock()
	if blockCount != 1 {
		return fmt.Errorf("必须且只能提供一种访问者类型配置")
	}
	if blockType != p.Type {
		return fmt.Errorf("访问者类型配置 %q 与类型 %q 不一致", blockType, p.Type)
	}
	return nil
}

func (p *VisitorDefinition) ToConfigurer() (v1.VisitorConfigurer, error) {
	block, _, _ := p.activeBlock()
	if block == nil {
		return nil, fmt.Errorf("必须且只能提供一种访问者类型配置")
	}

	cfg := block
	cfg.GetBaseConfig().Name = p.Name
	cfg.GetBaseConfig().Type = p.Type
	return cfg, nil
}

func VisitorDefinitionFromConfigurer(cfg v1.VisitorConfigurer) (VisitorDefinition, error) {
	if cfg == nil {
		return VisitorDefinition{}, fmt.Errorf("访问者配置为空")
	}

	base := cfg.GetBaseConfig()
	payload := VisitorDefinition{
		Name: base.Name,
		Type: base.Type,
	}

	switch c := cfg.(type) {
	case *v1.STCPVisitorConfig:
		payload.STCP = c
	case *v1.SUDPVisitorConfig:
		payload.SUDP = c
	case *v1.XTCPVisitorConfig:
		payload.XTCP = c
	default:
		return VisitorDefinition{}, fmt.Errorf("不支持的访问者配置类型 %T", cfg)
	}

	return payload, nil
}

func (p *VisitorDefinition) activeBlock() (v1.VisitorConfigurer, string, int) {
	count := 0
	var block v1.VisitorConfigurer
	var blockType string

	if p.STCP != nil {
		count++
		block = p.STCP
		blockType = "stcp"
	}
	if p.SUDP != nil {
		count++
		block = p.SUDP
		blockType = "sudp"
	}
	if p.XTCP != nil {
		count++
		block = p.XTCP
		blockType = "xtcp"
	}
	return block, blockType, count
}

func IsVisitorType(typ string) bool {
	switch typ {
	case "stcp", "sudp", "xtcp":
		return true
	default:
		return false
	}
}
