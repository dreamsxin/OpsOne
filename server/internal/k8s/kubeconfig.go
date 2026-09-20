// Package k8s 是一个只覆盖本平台所需能力的极简 Kubernetes 客户端。
//
// 为什么不用 client-go：平台目前只需要读若干列表和事件，client-go 会带进来
// 几十兆的依赖树。这里手写 REST 调用，和项目里手写 Jenkins REST、TLS 探测、
// TOTP 的做法一致，保持依赖表干净、可审。
package k8s

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

// kubeconfigFile 只解析我们用得到的字段，其余一律忽略
type kubeconfigFile struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKeyData         string `yaml:"client-key-data"`
			Token                 string `yaml:"token"`
			Username              string `yaml:"username"`
			Password              string `yaml:"password"`
		} `yaml:"user"`
	} `yaml:"users"`
}

// Config 一个可用的连接配置，由 kubeconfig 里选定的上下文拍平而来
type Config struct {
	ContextName string
	ClusterName string
	Server      string
	Namespace   string

	CAData     []byte
	CertData   []byte
	KeyData    []byte
	Token      string
	Username   string
	Password   string
	SkipVerify bool
}

// Contexts 列出 kubeconfig 里全部上下文名称，供界面上选择
func Contexts(raw string) ([]string, string, error) {
	var file kubeconfigFile
	if err := yaml.Unmarshal([]byte(raw), &file); err != nil {
		return nil, "", fmt.Errorf("kubeconfig 解析失败: %w", err)
	}
	names := make([]string, 0, len(file.Contexts))
	for _, item := range file.Contexts {
		names = append(names, item.Name)
	}
	return names, file.CurrentContext, nil
}

func decodeB64(label, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("%s 不是合法的 base64: %w", label, err)
	}
	return data, nil
}

// ParseKubeconfig 解析 kubeconfig 并拍平成指定上下文的连接配置。
// contextName 留空时用 current-context，没有 current-context 时用第一个上下文。
func ParseKubeconfig(raw, contextName string) (*Config, error) {
	var file kubeconfigFile
	if err := yaml.Unmarshal([]byte(raw), &file); err != nil {
		return nil, fmt.Errorf("kubeconfig 解析失败: %w", err)
	}
	if len(file.Clusters) == 0 {
		return nil, fmt.Errorf("kubeconfig 里没有 clusters")
	}
	if len(file.Contexts) == 0 {
		return nil, fmt.Errorf("kubeconfig 里没有 contexts")
	}

	if contextName == "" {
		contextName = file.CurrentContext
	}
	if contextName == "" {
		contextName = file.Contexts[0].Name
	}

	cfg := &Config{ContextName: contextName}
	var clusterRef, userRef string
	found := false
	for _, item := range file.Contexts {
		if item.Name == contextName {
			clusterRef, userRef = item.Context.Cluster, item.Context.User
			cfg.Namespace = item.Context.Namespace
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("kubeconfig 里没有上下文 %s", contextName)
	}

	found = false
	for _, item := range file.Clusters {
		if item.Name != clusterRef {
			continue
		}
		cfg.ClusterName = item.Name
		cfg.Server = strings.TrimRight(item.Cluster.Server, "/")
		cfg.SkipVerify = item.Cluster.InsecureSkipTLSVerify
		ca, err := decodeB64("certificate-authority-data", item.Cluster.CertificateAuthorityData)
		if err != nil {
			return nil, err
		}
		cfg.CAData = ca
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("上下文 %s 指向的集群 %s 不存在", contextName, clusterRef)
	}
	if cfg.Server == "" {
		return nil, fmt.Errorf("集群 %s 没有配置 server 地址", clusterRef)
	}

	// 用户段允许缺失：有些集群靠匿名访问或外部代理鉴权
	for _, item := range file.Users {
		if item.Name != userRef {
			continue
		}
		cert, err := decodeB64("client-certificate-data", item.User.ClientCertificateData)
		if err != nil {
			return nil, err
		}
		key, err := decodeB64("client-key-data", item.User.ClientKeyData)
		if err != nil {
			return nil, err
		}
		cfg.CertData, cfg.KeyData = cert, key
		cfg.Token = strings.TrimSpace(item.User.Token)
		cfg.Username, cfg.Password = item.User.Username, item.User.Password
		break
	}

	// 只支持嵌入式凭据：平台是集中纳管，不能依赖某台机器上的本地文件路径
	if cfg.CertData == nil && cfg.Token == "" && cfg.Username == "" {
		return nil, fmt.Errorf("上下文 %s 没有可用的嵌入式凭据，请使用含 client-certificate-data / token 的 kubeconfig（不支持指向本地文件的路径式凭据）", contextName)
	}
	if (cfg.CertData == nil) != (cfg.KeyData == nil) {
		return nil, fmt.Errorf("客户端证书与私钥必须同时提供")
	}
	if !strings.HasPrefix(cfg.Server, "https://") && !strings.HasPrefix(cfg.Server, "http://") {
		return nil, fmt.Errorf("server 地址必须以 http:// 或 https:// 开头")
	}
	return cfg, nil
}
