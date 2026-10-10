// SiYuan - From thought to insight, with agents
// Copyright (c) 2020-present, b3log.org
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package util

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"
)

func TestIsZhipuEndpoint(t *testing.T) {
	cases := []struct {
		baseURL string
		want    bool
	}{
		{"https://open.bigmodel.cn/api/paas/v4", true},
		{"https://open.bigmodel.cn/api/coding/paas/v4", true},
		{"https://bigmodel.cn/api/paas/v4", true},
		{"https://api.z.ai/api/paas/v4", true},
		{"https://z.ai/api/paas/v4", true},
		{"https://api.openai.com/v1", false},
		{"https://example.com/bigmodel.cn/api/paas/v4", false},
		{"https://generativelanguage.googleapis.com/v1beta/openai", false},
		{"http://localhost:11434/v1", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isZhipuEndpoint(tc.baseURL); got != tc.want {
			t.Errorf("isZhipuEndpoint(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestIsZhipuForcedThinkingModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"glm-5.3", true},
		{"GLM-5.3-Flash", true},
		{"glm-5.3-flashx", true},
		{"z-ai/glm-5.3", true},
		{"glm-4.7", true},
		{"glm-4.5v", true},
		{"glm-5.2", false},
		{"glm-5", false},
		{"glm-4.6", false},
		{"glm-4.5", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isZhipuForcedThinkingModel(tc.model); got != tc.want {
			t.Errorf("isZhipuForcedThinkingModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestRewriteZhipuChatBodyMaxTokensField(t *testing.T) {
	// 智谱端点不识别 max_completion_tokens，需要按上限语义转换为 max_tokens
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"1"}],` +
		`"max_completion_tokens":1,"temperature":1,"stream":false}`)
	rewritten, changed := rewriteZhipuChatBody(body, false)
	if !changed {
		t.Fatal("max_completion_tokens should be rewritten")
	}
	payload := map[string]any{}
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["max_completion_tokens"]; ok {
		t.Errorf("max_completion_tokens should be dropped, got %s", rewritten)
	}
	if tokens, ok := payload["max_tokens"].(float64); !ok || tokens != 1 {
		t.Errorf("max_tokens should carry the output cap, got %s", rewritten)
	}

	// 已显式设置 max_tokens 时保留调用方设置，仅移除无效字段
	body = []byte(`{"model":"glm-5.3","max_tokens":512,"max_completion_tokens":1}`)
	rewritten, changed = rewriteZhipuChatBody(body, false)
	if !changed {
		t.Fatal("max_completion_tokens should be dropped")
	}
	payload = map[string]any{}
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatal(err)
	}
	if tokens, _ := payload["max_tokens"].(float64); tokens != 512 {
		t.Errorf("max_tokens should keep the caller value, got %s", rewritten)
	}

	// 不含输出上限的请求体保持原样
	body = []byte(`{"model":"glm-4.6","messages":[]}`)
	if _, changed = rewriteZhipuChatBody(body, false); changed {
		t.Error("body without the output cap should not change")
	}
	// 非 JSON 请求体原样透传，适配不会破坏请求
	body = []byte("not json")
	if _, changed = rewriteZhipuChatBody(body, true); changed {
		t.Error("invalid body should not change")
	}
}

func TestRewriteZhipuChatBodyForcedThinking(t *testing.T) {
	cases := []struct {
		effort string
		want   string
	}{
		{"none", "low"},
		{"minimal", "low"},
		{"low", "low"},
		{"medium", "high"},
		{"high", "high"},
		{"xhigh", "max"},
		{"max", "max"},
	}
	for _, tc := range cases {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"disabled"},"reasoning_effort":"` + tc.effort + `"}`)
		rewritten, changed := rewriteZhipuChatBody(body, true)
		if !changed {
			t.Fatalf("effort %q should be rewritten", tc.effort)
		}
		payload := map[string]any{}
		if err := json.Unmarshal(rewritten, &payload); err != nil {
			t.Fatal(err)
		}
		thinking, _ := payload["thinking"].(map[string]any)
		if "enabled" != thinking["type"] {
			t.Errorf("effort %q: thinking should be enabled, got %s", tc.effort, rewritten)
		}
		if payload["reasoning_effort"] != tc.want {
			t.Errorf("effort %q should map to %q, got %s", tc.effort, tc.want, rewritten)
		}
	}

	// 调用方未指定推理强度时不补默认档位，沿用服务端默认
	rewritten, changed := rewriteZhipuChatBody([]byte(`{"model":"glm-5.3"}`), true)
	if !changed {
		t.Fatal("forced thinking model should enable thinking")
	}
	payload := map[string]any{}
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort should stay unset, got %s", rewritten)
	}

	// 非强制思考模型不改写思考参数
	body := []byte(`{"model":"glm-4.6","thinking":{"type":"disabled"},"reasoning_effort":"none"}`)
	rewritten, changed = rewriteZhipuChatBody(body, false)
	if changed || !bytes.Equal(rewritten, body) {
		t.Errorf("non forced thinking model should keep the body, got %s", rewritten)
	}
}

func TestZhipuChatCompatTransportRewritesChatRequest(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	transport := zhipuChatCompatTransportFor("https://open.bigmodel.cn/api/paas/v4", "glm-5.3", server.Client())
	if nil == transport {
		t.Fatal("zhipu endpoint should have a compatibility transport")
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/paas/v4/chat/completions",
		bytes.NewReader([]byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"1"}],`+
			`"max_completion_tokens":1,"temperature":1,"stream":false}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := transport.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err = io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{}
	if err = json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("captured request body is invalid: %s", err)
	}
	if _, ok := payload["max_completion_tokens"]; ok {
		t.Errorf("max_completion_tokens should not reach the endpoint, got %s", captured)
	}
	if tokens, _ := payload["max_tokens"].(float64); tokens != 1 {
		t.Errorf("output cap should be sent as max_tokens, got %s", captured)
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if "enabled" != thinking["type"] {
		t.Errorf("forced thinking model should enable thinking, got %s", captured)
	}

	// 模型清单等非生成请求原样透传
	captured = nil
	request, err = http.NewRequest(http.MethodGet, server.URL+"/api/paas/v4/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if response, err = transport.Do(request); err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err = io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if 0 != len(captured) {
		t.Errorf("non chat request should not carry a body, got %s", captured)
	}
}

func TestZhipuProbeParams(t *testing.T) {
	cases := []struct {
		protocol      string
		baseURL       string
		model         string
		wantTokens    int
		wantReasoning string
	}{
		{"openai", "https://open.bigmodel.cn/api/paas/v4", "glm-5.3", 1024, "low"},
		{"openai", "https://open.bigmodel.cn/api/paas/v4", "glm-5.3-flash", 1024, "low"},
		{"openai", "https://open.bigmodel.cn/api/paas/v4", "glm-4.6", 1, ""},
		{"openai", "https://api.openai.com/v1", "gpt-test", 1, ""},
		{"openai-responses", "https://open.bigmodel.cn/api/v1", "glm-5.3", 1, ""},
		{"anthropic-messages", "https://open.bigmodel.cn/api/anthropic", "glm-5.3", 1, ""},
	}
	for _, tc := range cases {
		tokens, reasoning := zhipuProbeParams(tc.protocol, tc.baseURL, tc.model)
		if tokens != tc.wantTokens || reasoning != tc.wantReasoning {
			t.Errorf("zhipuProbeParams(%q, %q, %q) = (%d, %q), want (%d, %q)",
				tc.protocol, tc.baseURL, tc.model, tokens, reasoning, tc.wantTokens, tc.wantReasoning)
		}
	}
}

func TestIsZhipuCodingPlanBillingError(t *testing.T) {
	billing := &openai.APIError{Message: "余额不足或无可用资源包,请充值", HTTPStatusCode: http.StatusTooManyRequests}
	if !IsZhipuCodingPlanBillingError("https://open.bigmodel.cn/api/paas/v4", billing) {
		t.Error("pay-as-you-go endpoint should report the coding plan rejection")
	}
	// 编码套餐端点已是正确地址，不再提示
	if IsZhipuCodingPlanBillingError("https://open.bigmodel.cn/api/coding/paas/v4", billing) {
		t.Error("coding endpoint should not report the coding plan rejection")
	}
	// 非智谱端点、其它错误与其它状态码都不提示
	if IsZhipuCodingPlanBillingError("https://api.openai.com/v1", billing) {
		t.Error("non zhipu endpoint should not report the coding plan rejection")
	}
	if IsZhipuCodingPlanBillingError("https://open.bigmodel.cn/api/paas/v4", errors.New("connection refused")) {
		t.Error("non API error should not report the coding plan rejection")
	}
	if IsZhipuCodingPlanBillingError("https://open.bigmodel.cn/api/paas/v4",
		&openai.APIError{Message: "余额不足或无可用资源包,请充值", HTTPStatusCode: http.StatusForbidden}) {
		t.Error("unexpected status code should not report the coding plan rejection")
	}
	if IsZhipuCodingPlanBillingError("https://open.bigmodel.cn/api/paas/v4",
		&openai.APIError{Message: "model not found", HTTPStatusCode: http.StatusTooManyRequests}) {
		t.Error("unrelated 429 should not report the coding plan rejection")
	}
}

func TestZhipuChatCompatTransportForOtherEndpoints(t *testing.T) {
	// 非智谱端点不启用适配，请求参数保持原样
	if transport := zhipuChatCompatTransportFor("https://api.openai.com/v1", "glm-5.3", http.DefaultClient); nil != transport {
		t.Error("non zhipu endpoint should not have a compatibility transport")
	}
	// 智谱端点上的非强制思考模型同样启用适配，由适配器按模型决定是否改写思考参数
	if transport := zhipuChatCompatTransportFor("https://open.bigmodel.cn/api/paas/v4", "glm-4.6", http.DefaultClient); nil == transport {
		t.Error("zhipu endpoint should have a compatibility transport")
	}
}
