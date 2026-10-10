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
	"net/url"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// 智谱开放平台（open.bigmodel.cn / api.z.ai）的 OpenAI 兼容端点与 OpenAI 存在两处差异：
// 1. 输出上限只识别 max_tokens，请求中的 max_completion_tokens 会被忽略，上层设置的上限随之静默失效；
// 2. GLM-5.3 系列等强制思考模型只接受 thinking.type=enabled，reasoning_effort 仅支持 low / high / max，
//    传入 none、medium 等档位会被服务端以参数错误（1210）拒绝。
// 两处差异都在请求体层面统一改写，使探活、Agent 与编辑器等调用方无需各自兼容，
// 详见 https://docs.bigmodel.cn/cn/guide/models/text/glm-5.3

// zhipuForcedThinkingModelPrefixes 列出强制开启思考的智谱模型族，含各档速度版本。
var zhipuForcedThinkingModelPrefixes = []string{"glm-5.3", "glm-4.7", "glm-4.5v"}

// zhipuProbeMaxCompletionTokens 是探活请求为强制思考模型预留的输出预算，官方建议输出上限不小于 1024。
const zhipuProbeMaxCompletionTokens = 1024

// isZhipuEndpoint 判断端点是否属于智谱开放平台，其 OpenAI 兼容端点需要额外的请求体适配。
func isZhipuEndpoint(apiBaseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(apiBaseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, domain := range []string{"bigmodel.cn", "z.ai"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// isZhipuForcedThinkingModel 判断模型是否强制开启思考，这类模型无法关闭思考且推理强度档位受限。
// 匹配大小写不敏感，并兼容带前缀的模型 id（如 z-ai/glm-5.3）。
func isZhipuForcedThinkingModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(normalized, '/'); slash >= 0 {
		normalized = normalized[slash+1:]
	}
	for _, prefix := range zhipuForcedThinkingModelPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// zhipuReasoningEffort 把推理强度收敛到强制思考模型支持的 low / high / max：
// 关闭思考的 none 与最低档 minimal 取最低档 low，xhigh 取最高档 max，其余档位保持原样由服务端校验。
func zhipuReasoningEffort(effort string) string {
	trimmed := strings.TrimSpace(effort)
	switch strings.ToLower(trimmed) {
	case "low", "high", "max":
		return trimmed
	case "none", "minimal":
		return "low"
	case "medium":
		return "high"
	case "xhigh":
		return "max"
	}
	return trimmed
}

// zhipuChatCompatTransport 为智谱端点改写 chat/completions 请求体，其余请求与端点原样透传。
type zhipuChatCompatTransport struct {
	base           openai.HTTPDoer
	forcedThinking bool
}

// zhipuChatCompatTransportFor 返回端点与模型对应的请求体适配器，非智谱端点返回 nil。
func zhipuChatCompatTransportFor(apiBaseURL, model string, base openai.HTTPDoer) openai.HTTPDoer {
	if !isZhipuEndpoint(apiBaseURL) {
		return nil
	}
	return &zhipuChatCompatTransport{base: base, forcedThinking: isZhipuForcedThinkingModel(model)}
}

// zhipuProbeParams 返回智谱端点探活请求的输出上限与推理档位。
// 强制思考模型无法关闭思考，1 个 token 的输出预算不足以完成推理，按官方建议预留推理预算并取最低推理档位，
// 避免测试请求本身被服务端拒绝；其余端点沿用极简请求。
func zhipuProbeParams(protocol, apiBaseURL, model string) (maxCompletionTokens int, reasoningEffort string) {
	if IsOpenAIResponsesProtocol(protocol) || IsAnthropicMessagesProtocol(protocol) {
		return 1, ""
	}
	if !isZhipuEndpoint(apiBaseURL) || !isZhipuForcedThinkingModel(model) {
		return 1, ""
	}
	return zhipuProbeMaxCompletionTokens, "low"
}

// IsZhipuCodingPlanBillingError 判断错误是否为智谱按量端点上的 GLM Coding Plan 订阅拒绝。
// 编码套餐 Key 只能在编码端点 /api/coding/paas/v4 调用，在按量端点会被以 1113 余额不足拒绝，
// 该提示语指向的解决办法见 https://docs.bigmodel.cn/cn/coding-plan/faq
func IsZhipuCodingPlanBillingError(apiBaseURL string, err error) bool {
	if nil == err || !isZhipuPayAsYouGoEndpoint(apiBaseURL) {
		return false
	}
	var apiErr *openai.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	message := apiErr.Message
	if !strings.Contains(message, "1113") && !strings.Contains(message, "余额不足") && !strings.Contains(message, "资源包") {
		return false
	}
	// 智谱以 429 表示余额或资源包不足，其他状态码不做提示
	return 0 == apiErr.HTTPStatusCode || http.StatusTooManyRequests == apiErr.HTTPStatusCode
}

// isZhipuPayAsYouGoEndpoint 判断端点是否为智谱按量计费的对话端点，编码套餐端点为 /api/coding/paas/v4。
func isZhipuPayAsYouGoEndpoint(apiBaseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(apiBaseURL))
	if err != nil || !isZhipuEndpoint(apiBaseURL) {
		return false
	}
	path := strings.TrimRight(parsed.Path, "/")
	return strings.HasSuffix(path, "/paas/v4") && !strings.Contains(path, "/coding/")
}

func (t *zhipuChatCompatTransport) Do(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.Body == nil || !strings.Contains(req.URL.Path, "chat/completions") {
		return t.base.Do(req)
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err = req.Body.Close(); err != nil {
		return nil, err
	}

	rewritten, changed := rewriteZhipuChatBody(body, t.forcedThinking)
	if !changed {
		rewritten = body
	}
	req.Body = io.NopCloser(bytes.NewReader(rewritten))
	req.ContentLength = int64(len(rewritten))
	return t.base.Do(req)
}

// rewriteZhipuChatBody 按智谱端点的参数差异改写请求体，返回改写后的数据以及是否发生改写。
// 请求体解析失败或无需改写时返回原始数据，保证请求不会被适配本身破坏。
func rewriteZhipuChatBody(body []byte, forcedThinking bool) ([]byte, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}

	changed := false
	if tokens, ok := payload["max_completion_tokens"]; ok {
		// 智谱端点不识别该字段，未设置 max_tokens 时按上限语义转换，避免输出上限失效。
		if _, exists := payload["max_tokens"]; !exists {
			payload["max_tokens"] = tokens
		}
		delete(payload, "max_completion_tokens")
		changed = true
	}
	if forcedThinking {
		// 强制思考模型不能关闭思考，显式开启并收敛推理强度，避免请求被参数校验拒绝。
		payload["thinking"] = map[string]any{"type": "enabled"}
		changed = true
		if effort, ok := payload["reasoning_effort"].(string); ok && strings.TrimSpace(effort) != "" {
			if mapped := zhipuReasoningEffort(effort); mapped != effort {
				payload["reasoning_effort"] = mapped
			}
		}
	}
	if !changed {
		return body, false
	}

	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return rewritten, true
}
