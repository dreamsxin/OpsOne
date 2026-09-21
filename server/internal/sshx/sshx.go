package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// MaxProxyDepth 跳板链最大层数，防止配置成环时无限递归
const MaxProxyDepth = 3

// Target 一次 SSH 连接所需的全部信息。
type Target struct {
	Address  string
	Port     int
	Username string
	AuthType string
	Secret   string // password 明文或私钥 PEM
	// Passphrase 私钥口令，空表示私钥未加密
	Passphrase string

	// PrepareError 调用方在组装本 Target 时就已经失败了（比如引用的共享凭据被禁用、
	// 密文解不开）。非空时一律不尝试连接，直接把这个原因抛回去 ——
	// 拿着空口令去连主机会表现成「认证失败」，把人引向错误的排查方向。
	PrepareError string

	// StrictHostKey 为 true 时校验主机公钥
	StrictHostKey bool
	// KnownHostKey 已记录的主机公钥（authorized_keys 格式），为空表示尚未记录
	KnownHostKey string
	// OnLearnHostKey 首次记录主机公钥时回调，由调用方负责持久化
	OnLearnHostKey func(authorizedKey string)

	// Proxy 跳板机，非空时先连跳板机再从其内部拨号到本目标
	Proxy *Target
}

func (t Target) addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Address, fmt.Sprint(port))
}

// Conn 包装 SSH 客户端，Close 时一并关闭跳板链上的连接
type Conn struct {
	*ssh.Client
	parent *Conn
}

func (c *Conn) Close() error {
	err := c.Client.Close()
	if c.parent != nil {
		_ = c.parent.Close()
	}
	return err
}

// Dial 建立到目标主机的 SSH 连接，按需经跳板机隧道。
func Dial(t Target, timeout time.Duration) (*Conn, error) {
	return dial(t, timeout, 0)
}

func dial(t Target, timeout time.Duration, depth int) (*Conn, error) {
	if depth > MaxProxyDepth {
		return nil, fmt.Errorf("跳板机层级超过 %d 层，请检查是否配置成环", MaxProxyDepth)
	}
	if t.PrepareError != "" {
		return nil, errors.New(t.PrepareError)
	}

	cfg, err := clientConfig(t, timeout)
	if err != nil {
		return nil, err
	}

	if t.Proxy == nil {
		client, err := ssh.Dial("tcp", t.addr(), cfg)
		if err != nil {
			return nil, err
		}
		return &Conn{Client: client}, nil
	}

	proxy, err := dial(*t.Proxy, timeout, depth+1)
	if err != nil {
		return nil, fmt.Errorf("连接跳板机 %s 失败: %w", t.Proxy.addr(), err)
	}

	// 在跳板机内部拨号到目标，再在该字节流上完成 SSH 握手
	tunnel, err := proxy.Client.Dial("tcp", t.addr())
	if err != nil {
		_ = proxy.Close()
		return nil, fmt.Errorf("跳板机无法连接目标 %s: %w", t.addr(), err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(tunnel, t.addr(), cfg)
	if err != nil {
		_ = tunnel.Close()
		_ = proxy.Close()
		return nil, err
	}
	return &Conn{Client: ssh.NewClient(sshConn, chans, reqs), parent: proxy}, nil
}

func clientConfig(t Target, timeout time.Duration) (*ssh.ClientConfig, error) {
	auth, err := authMethod(t)
	if err != nil {
		return nil, err
	}
	hostKey, err := hostKeyCallback(t)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            t.Username,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hostKey,
		Timeout:         timeout,
	}, nil
}

// hostKeyCallback 三种模式：
//  1. 关闭校验：接受任意主机公钥（默认，存在中间人风险）
//  2. 开启校验且已有记录：公钥必须与记录完全一致
//  3. 开启校验但无记录：首次连接信任并记录（TOFU）
func hostKeyCallback(t Target) (ssh.HostKeyCallback, error) {
	if !t.StrictHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	if t.KnownHostKey != "" {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(t.KnownHostKey))
		if err != nil {
			return nil, fmt.Errorf("已记录的主机公钥无法解析，请清空后重新探测: %w", err)
		}
		return ssh.FixedHostKey(pub), nil
	}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if t.OnLearnHostKey != nil {
			t.OnLearnHostKey(string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(key))))
		}
		return nil
	}, nil
}

func authMethod(t Target) (ssh.AuthMethod, error) {
	if t.AuthType == "key" {
		signer, err := parseKey(t)
		if err != nil {
			return nil, err
		}
		return ssh.PublicKeys(signer), nil
	}
	return ssh.Password(t.Secret), nil
}

// parseKey 解析私钥，带口令的私钥要用 Passphrase。
// 「私钥是加密的但没给口令」单独报错：默认的报错只说 parse failed，看不出缺的是口令。
func parseKey(t Target) (ssh.Signer, error) {
	if t.Passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase([]byte(t.Secret), []byte(t.Passphrase))
		if err == nil {
			return signer, nil
		}
		// 私钥其实没加密，却填了口令：这没有任何安全影响，按没口令再试一次，
		// 别拿「口令不对」去误导人（表单里留着一段旧口令是很常见的）
		if strings.Contains(err.Error(), "not password protected") {
			return parseKey(Target{AuthType: t.AuthType, Secret: t.Secret})
		}
		return nil, fmt.Errorf("用口令解析私钥失败（口令不对或私钥损坏）: %w", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(t.Secret))
	if err != nil {
		var need *ssh.PassphraseMissingError
		if errors.As(err, &need) {
			return nil, errors.New("这份私钥有口令保护，请在凭据里填写私钥口令")
		}
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}
	return signer, nil
}

// PublicKeyFingerprint 返回私钥对应公钥的 SHA256 指纹（ssh-keygen -lf 的那种格式）。
// 用来在界面上标识「这份凭据是哪把钥匙」而不泄露私钥本身。
func PublicKeyFingerprint(pem, passphrase string) (string, error) {
	signer, err := parseKey(Target{AuthType: "key", Secret: pem, Passphrase: passphrase})
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}

// Result 单次命令执行结果
type Result struct {
	Status   string
	ExitCode int
	Stdout   string
	Stderr   string
	CostMs   int64
}

// Run 在目标主机执行一条命令，超时由 ctx 控制。
func Run(ctx context.Context, t Target, command string) Result {
	start := time.Now()
	res := Result{Status: "failed", ExitCode: -1}

	dialTimeout := 10 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remain := time.Until(deadline); remain < dialTimeout {
			dialTimeout = remain
		}
	}

	client, err := Dial(t, dialTimeout)
	if err != nil {
		res.Stderr = err.Error()
		res.CostMs = time.Since(start).Milliseconds()
		return res
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		res.Stderr = err.Error()
		res.CostMs = time.Since(start).Milliseconds()
		return res
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		res.Status = "timeout"
		res.Stderr = "执行超时"
	case err := <-done:
		if err == nil {
			res.Status = "success"
			res.ExitCode = 0
		} else {
			var exitErr *ssh.ExitError
			if ok := asExitError(err, &exitErr); ok {
				res.ExitCode = exitErr.ExitStatus()
			}
			res.Stderr = stderr.String()
			if res.Stderr == "" {
				res.Stderr = err.Error()
			}
		}
	}

	res.Stdout = stdout.String()
	if res.Stderr == "" {
		res.Stderr = stderr.String()
	}
	res.CostMs = time.Since(start).Milliseconds()
	return res
}

func asExitError(err error, target **ssh.ExitError) bool {
	if e, ok := err.(*ssh.ExitError); ok {
		*target = e
		return true
	}
	return false
}
