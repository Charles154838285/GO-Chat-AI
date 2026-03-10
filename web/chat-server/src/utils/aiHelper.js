// AI功能相关的工具函数和状态管理

// AI用户UUID
export const AI_USER_UUID = 'ai-assistant-2024';

// 检查是否是AI用户
export function isAIUser(uuid) {
    return uuid === AI_USER_UUID;
}

// 检查消息是否包含@ai触发词
export function containsAIMention(content) {
    if (!content) return false;
    const lowerContent = content.toLowerCase().trim();
    return lowerContent.startsWith('@ai') || lowerContent.includes('@ai ');
}

// 提取@ai后的实际消息内容
export function extractAIMessage(content) {
    if (!content) return '';
    let processed = content.trim();
    processed = processed.replace(/^@ai\s*/i, '');
    return processed.trim();
}

// 获取AI用户信息
export async function getAIUserInfo(axios, backendUrl) {
    try {
        const response = await axios.get(backendUrl + '/ai/userInfo');
        if (response.data.code === 200) {
            return response.data.data;
        }
    } catch (error) {
        console.error('获取AI用户信息失败:', error);
    }
    return null;
}

// 获取AI服务状态
export async function getAIStatus(axios, backendUrl) {
    try {
        const response = await axios.get(backendUrl + '/ai/status');
        if (response.data.code === 200) {
            return response.data.data;
        }
    } catch (error) {
        console.error('获取AI状态失败:', error);
    }
    return { enabled: false };
}

// 清除AI上下文
export async function clearAIContext(axios, backendUrl, userId) {
    try {
        const response = await axios.post(backendUrl + '/ai/clearContext', null, {
            params: { user_id: userId }
        });
        return response.data.code === 200;
    } catch (error) {
        console.error('清除AI上下文失败:', error);
        return false;
    }
}

// AI消息样式标识
export function getAIMessageClass(senderId) {
    return isAIUser(senderId) ? 'ai-message' : '';
}

// 格式化AI消息（可以添加特殊格式，比如Markdown渲染）
export function formatAIMessage(content) {
    // 这里可以添加更多格式化逻辑，比如代码高亮、链接识别等
    return content;
}
