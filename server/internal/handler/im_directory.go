package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IM 通讯录读取：企业微信 / 钉钉 / 飞书的「企业自建应用」接口适配。
//
// 与「通知渠道」里的群机器人完全是两套东西：机器人只有一个 webhook 地址，
// 只能往群里发消息；这里用的是应用级凭据（corpid + secret），能读部门与成员。
//
// 三家共同的坑（群机器人那轮踩过一次，这里同样适用）：
// **HTTP 200 不代表成功** —— 密钥错、没通讯录权限、超频都会返回 200 加一个非零
// errcode（飞书是 code）。所以每个适配器都先看 body 里的错误码，而不是只看 HTTP 状态。
//
// 一个必须说清楚的边界：这些路径与字段名来自各家公开文档，**没有在真实企业租户上验过**
// （手上没有企业应用凭据）。因此所有错误都把上游原话带出来，便于按厂商文档对照；
// 地址也做成可覆盖的（ImApp.BaseURL），既方便对着私有代理调，也方便用假上游验证逻辑。
// 见 docs/SECURITY.md 23。

const (
	// imDirTimeout 读通讯录的超时。一次同步要拉几十个部门，单请求别拖太久
	imDirTimeout = 20 * time.Second
	// imDirBodyLimit 单个响应体上限：几千人的成员列表也就几 MB
	imDirBodyLimit = 8 << 20
	// imDirMaxDepts 一次同步最多处理多少个部门，防止把整个集团几千个部门拉进来
	imDirMaxDepts = 500
)

// 各家默认地址。BaseURL 留空时用它们。
const (
	wecomDefaultBase    = "https://qyapi.weixin.qq.com"
	dingtalkDefaultBase = "https://oapi.dingtalk.com"
	feishuDefaultBase   = "https://open.feishu.cn"
)

// imDept IM 侧的一个部门
type imDept struct {
	ID       string
	ParentID string
	Name     string
	Order    int
}

// imUser IM 侧的一个成员
type imUser struct {
	ID     string
	Name   string
	Email  string
	Mobile string
	// DeptIDs 所属部门，第一个当作主部门
	DeptIDs []string
	// Active IM 侧是否在职。取不到时按在职处理，不替厂商猜
	Active bool
}

// imDirectory 一家 IM 的通讯录读取能力
type imDirectory interface {
	// token 取访问令牌
	token(ctx context.Context, corpID, secret string) (string, error)
	// departments 从 root 开始拉部门子树
	departments(ctx context.Context, token, root string) ([]imDept, error)
	// users 拉某个部门下的成员
	users(ctx context.Context, token, deptID string) ([]imUser, error)
	// label 出错时带上是哪家，便于按厂商文档查错误码
	label() string
}

func imDirectoryFor(provider, base string, client *http.Client) (imDirectory, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	switch provider {
	case channelWecom:
		if base == "" {
			base = wecomDefaultBase
		}
		return wecomDirectory{base: base, client: client}, nil
	case channelDingTalk:
		if base == "" {
			base = dingtalkDefaultBase
		}
		return dingtalkDirectory{base: base, client: client}, nil
	case channelFeishu:
		if base == "" {
			base = feishuDefaultBase
		}
		return feishuDirectory{base: base, client: client}, nil
	}
	return nil, fmt.Errorf("不支持的 IM 类型 %s", provider)
}

// ---------- HTTP 小工具 ----------

func imGetJSON(ctx context.Context, client *http.Client, rawURL, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return imDoJSON(client, req, out)
}

func imPostJSON(ctx context.Context, client *http.Client, rawURL, bearer string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return imDoJSON(client, req, out)
}

// imDoJSON 发请求。client 由调用方给（业务侧一律传 h.egressClient，走统一出口）；
// 传 nil 时退回一个不带代理设置的临时 client —— 只有测试会走到这条路
func imDoJSON(client *http.Client, req *http.Request, out any) error {
	if client == nil {
		client = &http.Client{Timeout: imDirTimeout} // egress-exempt: 测试兜底
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imDirBodyLimit))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("上游返回 HTTP %d: %s", resp.StatusCode,
			truncate(strings.TrimSpace(string(raw)), 300))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("响应不是合法 JSON: %s", truncate(strings.TrimSpace(string(raw)), 200))
	}
	return nil
}

// imErrEnvelope 企微与钉钉共用的错误信封（飞书是 code/msg）
type imErrEnvelope struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// check errcode 非 0 即失败，哪怕 HTTP 是 200
func (e imErrEnvelope) check(label string) error {
	if e.ErrCode == 0 {
		return nil
	}
	return fmt.Errorf("%s 返回 errcode=%d: %s", label, e.ErrCode, e.ErrMsg)
}

// ---------- 企业微信 ----------

type wecomDirectory struct {
	base   string
	client *http.Client
}

func (wecomDirectory) label() string { return "企业微信" }

func (d wecomDirectory) token(ctx context.Context, corpID, secret string) (string, error) {
	var out struct {
		imErrEnvelope
		AccessToken string `json:"access_token"`
	}
	target := d.base + "/cgi-bin/gettoken?corpid=" + url.QueryEscape(corpID) +
		"&corpsecret=" + url.QueryEscape(secret)
	if err := imGetJSON(ctx, d.client, target, "", &out); err != nil {
		return "", err
	}
	if err := out.check(d.label()); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("企业微信没有返回 access_token")
	}
	return out.AccessToken, nil
}

func (d wecomDirectory) departments(ctx context.Context, token, root string) ([]imDept, error) {
	var out struct {
		imErrEnvelope
		Department []struct {
			ID       int    `json:"id"`
			ParentID int    `json:"parentid"`
			Name     string `json:"name"`
			Order    int    `json:"order"`
		} `json:"department"`
	}
	target := d.base + "/cgi-bin/department/list?access_token=" + url.QueryEscape(token)
	if strings.TrimSpace(root) != "" {
		target += "&id=" + url.QueryEscape(root)
	}
	if err := imGetJSON(ctx, d.client, target, "", &out); err != nil {
		return nil, err
	}
	if err := out.check(d.label()); err != nil {
		return nil, err
	}
	list := make([]imDept, 0, len(out.Department))
	for _, item := range out.Department {
		list = append(list, imDept{
			ID: fmt.Sprint(item.ID), ParentID: fmt.Sprint(item.ParentID),
			Name: item.Name, Order: item.Order,
		})
	}
	return list, nil
}

func (d wecomDirectory) users(ctx context.Context, token, deptID string) ([]imUser, error) {
	var out struct {
		imErrEnvelope
		UserList []struct {
			UserID     string `json:"userid"`
			Name       string `json:"name"`
			Email      string `json:"email"`
			Mobile     string `json:"mobile"`
			Department []int  `json:"department"`
			// Status 1=已激活 2=已禁用 4=未激活 5=退出企业
			Status int `json:"status"`
		} `json:"userlist"`
	}
	// fetch_child=0：部门树自己递归，这里只取本部门，免得同一个人被拉很多遍
	target := d.base + "/cgi-bin/user/list?access_token=" + url.QueryEscape(token) +
		"&department_id=" + url.QueryEscape(deptID) + "&fetch_child=0"
	if err := imGetJSON(ctx, d.client, target, "", &out); err != nil {
		return nil, err
	}
	if err := out.check(d.label()); err != nil {
		return nil, err
	}
	list := make([]imUser, 0, len(out.UserList))
	for _, item := range out.UserList {
		depts := make([]string, 0, len(item.Department))
		for _, id := range item.Department {
			depts = append(depts, fmt.Sprint(id))
		}
		list = append(list, imUser{
			ID: item.UserID, Name: item.Name, Email: item.Email, Mobile: item.Mobile,
			DeptIDs: depts,
			// 2=已禁用、5=退出企业 当作不在职；1/4 都算在职（未激活的人也还在编）
			Active: item.Status != 2 && item.Status != 5,
		})
	}
	return list, nil
}

// ---------- 钉钉 ----------

type dingtalkDirectory struct {
	base   string
	client *http.Client
}

func (dingtalkDirectory) label() string { return "钉钉" }

func (d dingtalkDirectory) token(ctx context.Context, appKey, secret string) (string, error) {
	var out struct {
		imErrEnvelope
		AccessToken string `json:"access_token"`
	}
	// 钉钉这里 corpID 字段里放的是 appkey
	target := d.base + "/gettoken?appkey=" + url.QueryEscape(appKey) +
		"&appsecret=" + url.QueryEscape(secret)
	if err := imGetJSON(ctx, d.client, target, "", &out); err != nil {
		return "", err
	}
	if err := out.check(d.label()); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("钉钉没有返回 access_token")
	}
	return out.AccessToken, nil
}

func (d dingtalkDirectory) departments(ctx context.Context, token, root string) ([]imDept, error) {
	if strings.TrimSpace(root) == "" {
		root = "1"
	}
	// 钉钉只能一层层往下取，自己做广度遍历
	queue := []string{root}
	seen := map[string]bool{}
	list := make([]imDept, 0, 16)
	for len(queue) > 0 && len(list) < imDirMaxDepts {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}
		seen[current] = true

		var out struct {
			imErrEnvelope
			Result []struct {
				DeptID   int64  `json:"dept_id"`
				ParentID int64  `json:"parent_id"`
				Name     string `json:"name"`
			} `json:"result"`
		}
		target := d.base + "/topapi/v2/department/listsub?access_token=" + url.QueryEscape(token)
		if err := imPostJSON(ctx, d.client, target, "", map[string]any{"dept_id": current}, &out); err != nil {
			return nil, err
		}
		if err := out.check(d.label()); err != nil {
			return nil, err
		}
		for _, item := range out.Result {
			id := fmt.Sprint(item.DeptID)
			list = append(list, imDept{
				ID: id, ParentID: fmt.Sprint(item.ParentID), Name: item.Name,
			})
			queue = append(queue, id)
		}
	}
	return list, nil
}

func (d dingtalkDirectory) users(ctx context.Context, token, deptID string) ([]imUser, error) {
	list := make([]imUser, 0, 16)
	cursor := 0
	for {
		var out struct {
			imErrEnvelope
			Result struct {
				HasMore    bool `json:"has_more"`
				NextCursor int  `json:"next_cursor"`
				List       []struct {
					UserID  string  `json:"userid"`
					Name    string  `json:"name"`
					Email   string  `json:"email"`
					Mobile  string  `json:"mobile"`
					DeptIDs []int64 `json:"dept_id_list"`
					Active  bool    `json:"active"`
				} `json:"list"`
			} `json:"result"`
		}
		target := d.base + "/topapi/v2/user/list?access_token=" + url.QueryEscape(token)
		body := map[string]any{"dept_id": deptID, "cursor": cursor, "size": 100}
		if err := imPostJSON(ctx, d.client, target, "", body, &out); err != nil {
			return nil, err
		}
		if err := out.check(d.label()); err != nil {
			return nil, err
		}
		for _, item := range out.Result.List {
			depts := make([]string, 0, len(item.DeptIDs))
			for _, id := range item.DeptIDs {
				depts = append(depts, fmt.Sprint(id))
			}
			list = append(list, imUser{
				ID: item.UserID, Name: item.Name, Email: item.Email, Mobile: item.Mobile,
				DeptIDs: depts, Active: item.Active,
			})
		}
		if !out.Result.HasMore {
			break
		}
		cursor = out.Result.NextCursor
	}
	return list, nil
}

// ---------- 飞书 ----------

type feishuDirectory struct {
	base   string
	client *http.Client
}

func (feishuDirectory) label() string { return "飞书" }

func (d feishuDirectory) token(ctx context.Context, appID, secret string) (string, error) {
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	target := d.base + "/open-apis/auth/v3/tenant_access_token/internal"
	body := map[string]any{"app_id": appID, "app_secret": secret}
	if err := imPostJSON(ctx, d.client, target, "", body, &out); err != nil {
		return "", err
	}
	// 飞书用 code/msg 而不是 errcode/errmsg，同样是 200 里藏错误
	if out.Code != 0 {
		return "", fmt.Errorf("飞书返回 code=%d: %s", out.Code, out.Msg)
	}
	if out.TenantAccessToken == "" {
		return "", fmt.Errorf("飞书没有返回 tenant_access_token")
	}
	return out.TenantAccessToken, nil
}

func (d feishuDirectory) departments(ctx context.Context, token, root string) ([]imDept, error) {
	if strings.TrimSpace(root) == "" {
		root = "0"
	}
	list := make([]imDept, 0, 16)
	pageToken := ""
	for {
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
				Items     []struct {
					DepartmentID       string `json:"department_id"`
					ParentDepartmentID string `json:"parent_department_id"`
					Name               string `json:"name"`
				} `json:"items"`
			} `json:"data"`
		}
		target := d.base + "/open-apis/contact/v3/departments?department_id=" +
			url.QueryEscape(root) + "&fetch_child=true&page_size=50&department_id_type=department_id"
		if pageToken != "" {
			target += "&page_token=" + url.QueryEscape(pageToken)
		}
		if err := imGetJSON(ctx, d.client, target, token, &out); err != nil {
			return nil, err
		}
		if out.Code != 0 {
			return nil, fmt.Errorf("飞书返回 code=%d: %s", out.Code, out.Msg)
		}
		for _, item := range out.Data.Items {
			list = append(list, imDept{
				ID: item.DepartmentID, ParentID: item.ParentDepartmentID, Name: item.Name,
			})
		}
		if !out.Data.HasMore || out.Data.PageToken == "" || len(list) >= imDirMaxDepts {
			break
		}
		pageToken = out.Data.PageToken
	}
	return list, nil
}

func (d feishuDirectory) users(ctx context.Context, token, deptID string) ([]imUser, error) {
	list := make([]imUser, 0, 16)
	pageToken := ""
	for {
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
				Items     []struct {
					OpenID        string   `json:"open_id"`
					Name          string   `json:"name"`
					Email         string   `json:"email"`
					Mobile        string   `json:"mobile"`
					DepartmentIDs []string `json:"department_ids"`
					Status        struct {
						IsActivated bool `json:"is_activated"`
						IsResigned  bool `json:"is_resigned"`
					} `json:"status"`
				} `json:"items"`
			} `json:"data"`
		}
		target := d.base + "/open-apis/contact/v3/users/find_by_department?department_id=" +
			url.QueryEscape(deptID) + "&page_size=50&department_id_type=department_id"
		if pageToken != "" {
			target += "&page_token=" + url.QueryEscape(pageToken)
		}
		if err := imGetJSON(ctx, d.client, target, token, &out); err != nil {
			return nil, err
		}
		if out.Code != 0 {
			return nil, fmt.Errorf("飞书返回 code=%d: %s", out.Code, out.Msg)
		}
		for _, item := range out.Data.Items {
			list = append(list, imUser{
				ID: item.OpenID, Name: item.Name, Email: item.Email, Mobile: item.Mobile,
				DeptIDs: item.DepartmentIDs,
				// 离职的人明确排除；未激活的仍算在职
				Active: !item.Status.IsResigned,
			})
		}
		if !out.Data.HasMore || out.Data.PageToken == "" {
			break
		}
		pageToken = out.Data.PageToken
	}
	return list, nil
}
