import type {AIModelTestData} from "../../../types/api";

export interface IProviderTestResult {
    success: boolean;
    modelNotFound: boolean;
    message: string;
}

// 连接测试结果判定：服务端错误优先于「模型不在可用清单」的判断，
// 避免生成请求失败时被误报为模型名称不在清单中。
export const resolveProviderTestResult = (data: AIModelTestData): IProviderTestResult => {
    const message = data.msg ? String(data.msg) : "";
    if (data.matched) {
        return {success: true, modelNotFound: false, message};
    }
    return {
        success: false,
        modelNotFound: !message && Array.isArray(data.available) && data.available.length > 0,
        message,
    };
};
